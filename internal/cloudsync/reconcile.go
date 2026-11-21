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
			pending[file.TaskID] = append(pending[file.TaskID], file)
		}
	}
	if len(pending) == 0 {
		return firstError
	}
	dirs, err := s.client.List(ctx, s.opts.TargetCID)
	if err != nil {
		return errors.Join(firstError, fmt.Errorf("read 115 target directory: %w", err))
	}
	byName := indexByName(dirs)
	for taskID, files := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		matches := byName[taskID]
		if len(matches) == 0 {
			continue // CD2 has not created this remote task directory yet.
		}
		if len(matches) != 1 || !matches[0].IsDir || matches[0].ID == "" || matches[0].ParentID != s.opts.TargetCID {
			addError(fmt.Errorf("ambiguous 115 directory for task %s", taskID))
			continue
		}
		addError(s.confirmDirectory(ctx, matches[0].ID, files))
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
		name, err := relativeName(file)
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

func relativeName(file state.CloudFile) (string, error) {
	task, name, ok := strings.Cut(file.RelativePath, "/")
	if !ok || task != file.TaskID || !safeComponent(task) || !safeComponent(name) {
		return "", errors.New("invalid exported 115 audio path")
	}
	return name, nil
}

func (s *Service) export(file state.CloudFile) error {
	if s.opts.STRMDir == "" {
		return nil
	}
	name, err := relativeName(file)
	if err != nil {
		return err
	}
	body := strings.TrimRight(s.opts.MediaBaseURL, "/") + "/media/" + file.MediaID + "\n"
	if err := writeSTRM(s.opts.STRMDir, file.TaskID, name+".strm", body); err != nil {
		return fmt.Errorf("export STRM for task %s track %s: %w", file.TaskID, file.TrackID, err)
	}
	return nil
}

func writeSTRM(rootDir, taskID, name, body string) error {
	if err := ensureTaskDirectory(rootDir, taskID); err != nil {
		return err
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	dest := filepath.Join(taskID, name)
	if info, err := root.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("STRM destination must be a regular file")
		}
		if info.Size() == int64(len(body)) {
			existing, err := root.Open(dest)
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
	temp := filepath.Join(taskID, ".meloport-"+randomID()+".tmp")
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
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
	return root.Rename(temp, dest)
}
