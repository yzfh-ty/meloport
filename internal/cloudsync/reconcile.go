package cloudsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"meloport/internal/cloud115"
	"meloport/internal/state"
)

// Reconcile checks uploaded entries even after their playlist or task is gone.
// Already-confirmed files can repair missing STRM exports without a 115 call.
func (s *Service) Reconcile(ctx context.Context) error {
	select {
	case s.reconcile <- struct{}{}:
		defer func() { <-s.reconcile }()
	case <-ctx.Done():
		return ctx.Err()
	}
	files, err := s.store.ListCloudFiles(ctx, s.opts.TargetCID)
	if err != nil {
		return err
	}
	pending := make(map[string][]state.CloudFile)
	var firstError error
	addError := func(err error) {
		if firstError == nil {
			firstError = err
		}
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.ID != "" {
			addError(s.export(file))
			continue
		}
		handedOff, err := s.hasHandedOff(ctx, file)
		if err != nil {
			addError(err)
			continue
		}
		if handedOff {
			folder, _, pathErr := relativeName(file)
			if pathErr != nil {
				addError(pathErr)
				continue
			}
			pending[folder] = append(pending[folder], file)
		}
	}
	if len(pending) == 0 {
		return firstError
	}
	dirs, err := s.client.List(ctx, s.opts.TargetCID)
	if err != nil {
		return errors.Join(firstError, fmt.Errorf("read 115 target directory: %w", err))
	}
	for folder, files := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		parentCID := s.opts.TargetCID
		segments := strings.Split(folder, "/")
		missing := false
		for _, segment := range segments {
			entries := dirs
			if parentCID != s.opts.TargetCID {
				var listErr error
				entries, listErr = s.client.List(ctx, parentCID)
				if listErr != nil {
					addError(fmt.Errorf("read 115 collection directory: %w", listErr))
					missing = true
					break
				}
			}
			matches := indexByName(entries)[segment]
			if len(matches) == 0 {
				missing = true // CD2 has not created this collection directory yet.
				break
			}
			if len(matches) != 1 || !matches[0].IsDir || matches[0].ID == "" || matches[0].ParentID != parentCID {
				addError(fmt.Errorf("ambiguous 115 directory for collection %s", folder))
				missing = true
				break
			}
			parentCID = matches[0].ID
		}
		if !missing {
			addError(s.confirmDirectory(ctx, parentCID, files))
		}
	}
	return firstError
}

func indexByName(files []cloud115.File) map[string][]cloud115.File {
	byName := make(map[string][]cloud115.File, len(files))
	for _, file := range files {
		byName[file.Name] = append(byName[file.Name], file)
	}
	return byName
}

func (s *Service) confirmDirectory(ctx context.Context, cid string, files []state.CloudFile) error {
	entries, err := s.client.List(ctx, cid)
	if err != nil {
		return fmt.Errorf("read 115 task directory: %w", err)
	}
	byName := indexByName(entries)
	var firstError error
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, name, err := relativeName(file)
		if err == nil {
			matches := byName[name]
			if len(matches) == 0 {
				continue // Absence is normal while waiting for CD2 to upload.
			}
			err = s.confirm(ctx, file, cid, matches)
		}
		if firstError == nil {
			firstError = err
		}
	}
	return firstError
}

func (s *Service) confirm(ctx context.Context, file state.CloudFile, cid string, matches []cloud115.File) error {
	remote := matches[0]
	if len(matches) != 1 || remote.IsDir || remote.Incomplete || remote.ID == "" || remote.PickCode == "" || remote.ParentID != cid ||
		remote.Size != file.Size || !strings.EqualFold(remote.SHA1, file.SHA1) {
		return fmt.Errorf("115 file for task %s track %s does not uniquely match the staged audio", file.TaskID, file.TrackID)
	}
	file.CID, file.ID, file.PickCode = cid, remote.ID, remote.PickCode
	// Do not confirm an interrupted intent while Stage is retrying its move.
	// The comparison in ConfirmCloudFile also rejects a replaced intent.
	select {
	case s.stage <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := s.store.ConfirmCloudFile(ctx, file)
	<-s.stage
	if err != nil {
		return err
	}
	return s.export(file)
}

func relativeName(file state.CloudFile) (string, string, error) {
	parts := strings.Split(file.RelativePath, "/")
	if len(parts) == 2 {
		if parts[0] != file.TaskID || !safeComponent(parts[0]) || !safeComponent(parts[1]) {
			return "", "", errors.New("invalid exported 115 audio path")
		}
		return parts[0], parts[1], nil // Legacy exports remain repairable.
	}
	if len(parts) != 3 || parts[0] != file.Provider || !safeComponent(parts[0]) || !safeComponent(parts[1]) || !safeComponent(parts[2]) {
		return "", "", errors.New("invalid exported 115 audio path")
	}
	return parts[0] + "/" + parts[1], parts[2], nil
}

func (s *Service) export(file state.CloudFile) error {
	if s.opts.STRMDir == "" {
		return nil
	}
	folder, name, err := relativeName(file)
	if err != nil {
		return err
	}
	body := strings.TrimRight(s.opts.MediaBaseURL, "/") + "/media/" + file.MediaID + "\n"
	if err := writeSTRM(s.opts.STRMDir, folder, name+".strm", body); err != nil {
		return fmt.Errorf("export STRM for task %s track %s: %w", file.TaskID, file.TrackID, err)
	}
	return nil
}

func writeSTRM(rootDir, folder, name, body string) error {
	parts := strings.Split(folder, "/")
	if len(parts) < 1 || len(parts) > 2 || !safeComponent(name) {
		return errors.New("invalid STRM collection path")
	}
	for _, part := range parts {
		if !safeComponent(part) {
			return errors.New("invalid STRM collection path")
		}
	}
	if len(parts) == 1 {
		if err := ensureTaskDirectory(rootDir, parts[0]); err != nil {
			return err
		}
	} else if err := ensureCollectionDirectory(rootDir, parts[0], parts[1]); err != nil {
		return err
	}
	destPath := filepath.Join(rootDir, folder, name)
	if info, err := os.Lstat(destPath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("STRM destination must be a regular file")
		}
		if info.Size() == int64(len(body)) {
			existing, err := os.Open(destPath)
			if err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(existing, int64(len(body)+1)))
			_ = existing.Close()
			if err != nil || string(data) == body {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp := ".meloport-" + randomID() + ".tmp"
	tempPath := filepath.Join(rootDir, folder, temp)
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tempPath)
	defer file.Close()
	if _, err := io.WriteString(file, body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, destPath)
}
