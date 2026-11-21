// Package cloudsync hands complete audio files to CD2 and confirms their 115 mappings.
package cloudsync

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"meloport/internal/cloud115"
	"meloport/internal/state"
)

type DirectoryReader interface {
	List(context.Context, string) ([]cloud115.File, error)
}

type Options struct {
	TargetCID, StagingDir, STRMDir, MediaBaseURL string
}

type Service struct {
	store     *state.Store
	client    DirectoryReader
	opts      Options
	stage     chan struct{}
	reconcile chan struct{}
}

func New(store *state.Store, client DirectoryReader, opts Options) *Service {
	return &Service{store: store, client: client, opts: opts, stage: make(chan struct{}, 1), reconcile: make(chan struct{}, 1)}
}

// Ready reports whether a new download should wait for CD2 or 115.
// A crash before the move normally leaves the working file available to retry.
func (s *Service) Ready(ctx context.Context, taskID, provider, trackID string) (bool, error) {
	select {
	case s.stage <- struct{}{}:
		defer func() { <-s.stage }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	file, exists, err := s.store.GetCloudFile(ctx, s.opts.TargetCID, taskID, provider, trackID)
	if err != nil || !exists {
		return false, err
	}
	return s.hasHandedOff(ctx, file)
}

func (s *Service) hasHandedOff(ctx context.Context, file state.CloudFile) (bool, error) {
	if file.Published || file.ID != "" {
		return true, nil
	}
	// The working file remains in place until the move succeeds.
	if info, err := os.Lstat(file.LocalPath); err == nil {
		if !info.Mode().IsRegular() {
			return false, errors.New("working 115 audio path is not a regular file")
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, errors.New("cannot inspect working audio for 115 handoff")
	}
	if info, err := os.Lstat(file.StagingPath); err == nil {
		if !info.Mode().IsRegular() || info.Size() != file.Size {
			return false, errors.New("CD2 staging file changed during handoff")
		}
	} else {
		if errors.Is(err, os.ErrNotExist) {
			// CD2 may have uploaded and consumed the file before publication
			// was recorded. Wait for a remote match without asserting a move.
			return true, nil
		}
		return false, errors.New("cannot inspect CD2 staging file")
	}
	if err := s.store.MarkCloudPublished(ctx, file); err != nil {
		return false, err
	}
	return true, nil
}

// Stage atomically moves verified tagged audio into CD2's watched directory.
// The working and staging directories must be on the same filesystem.
func (s *Service) Stage(ctx context.Context, item state.Download) (bool, error) {
	select {
	case s.stage <- struct{}{}:
		defer func() { <-s.stage }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	existing, found, err := s.store.GetCloudFile(ctx, s.opts.TargetCID, item.TaskID, item.Provider, item.TrackID)
	if err != nil {
		return false, err
	}
	if found {
		ready, err := s.hasHandedOff(ctx, existing)
		if err != nil || ready {
			return false, err
		}
	}
	name := filepath.Base(item.Path)
	collection := item.Collection
	legacyFolder := collection == ""
	if legacyFolder {
		collection = item.TaskID // Legacy callers retain their old folder layout.
	}
	if (!legacyFolder && !safeComponent(item.Provider)) || !safeComponent(collection) || !safeComponent(name) || !filepath.IsAbs(item.Path) || item.Size <= 0 || item.Size == 1<<63-1 {
		return false, errors.New("invalid 115 staging audio record")
	}
	if item.TagPending || item.TagFingerprint == "" {
		return false, errors.New("audio tags must finish before CD2 staging")
	}
	sha1sum, info, err := fingerprint(ctx, item)
	if err != nil {
		return false, err
	}
	folder := collection
	if legacyFolder {
		if err := ensureTaskDirectory(s.opts.StagingDir, collection); err != nil {
			return false, fmt.Errorf("prepare CD2 staging directory: %w", err)
		}
	} else {
		folder = filepath.Join(item.Provider, collection)
	}
	if !legacyFolder {
		if err := ensureCollectionDirectory(s.opts.StagingDir, item.Provider, collection); err != nil {
			return false, fmt.Errorf("prepare CD2 staging directory: %w", err)
		}
	}
	stagedPath := filepath.Join(s.opts.StagingDir, folder, name)
	if _, err := os.Lstat(stagedPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return false, errors.New("CD2 staging already contains the audio filename; working file was preserved")
		}
		return false, errors.New("cannot inspect CD2 staging destination")
	}
	file, err := s.store.SaveCloudIntent(ctx, state.CloudFile{
		RootCID: s.opts.TargetCID, TaskID: item.TaskID, Provider: item.Provider, TrackID: item.TrackID,
		LocalPath: item.Path, StagingPath: stagedPath, RelativePath: filepath.ToSlash(filepath.Join(folder, name)),
		Size: item.Size, SHA1: sha1sum, MediaID: randomID(),
	})
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	current, err := os.Lstat(item.Path)
	if err != nil || !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
		return false, errors.New("working audio changed before CD2 handoff")
	}
	if err := moveStaged(item.Path, stagedPath); err != nil {
		return false, fmt.Errorf("move audio to CD2 staging; check directory access and same-filesystem paths: %w", err)
	}
	return true, s.store.MarkCloudPublished(ctx, file)
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func fingerprint(ctx context.Context, item state.Download) (string, os.FileInfo, error) {
	info, err := os.Lstat(item.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
		return "", nil, errors.New("working audio is missing or changed before CD2 staging")
	}
	file, err := os.Open(item.Path)
	if err != nil {
		return "", nil, errors.New("cannot open working audio for 115 verification")
	}
	defer file.Close()
	if opened, err := file.Stat(); err != nil || !os.SameFile(info, opened) {
		return "", nil, errors.New("working audio changed before 115 verification")
	}
	// 115 uses SHA-1 as its file fingerprint. The local SHA-256 is still
	// checked to ensure the staged bytes match the download database record.
	remoteDigest, localDigest := sha1.New(), sha256.New()
	n, err := io.Copy(io.MultiWriter(remoteDigest, localDigest), io.LimitReader(contextReader{ctx, file}, item.Size+1))
	if err != nil {
		return "", nil, fmt.Errorf("verify working audio for 115: %w", err)
	}
	if n != item.Size || !strings.EqualFold(hex.EncodeToString(localDigest.Sum(nil)), item.SHA256) {
		return "", nil, errors.New("working audio does not match its download record")
	}
	return hex.EncodeToString(remoteDigest.Sum(nil)), info, nil
}

func ensureTaskDirectory(rootDir, taskID string) error {
	if !filepath.IsAbs(rootDir) || !safeComponent(taskID) {
		return errors.New("invalid task directory path")
	}
	if err := os.MkdirAll(rootDir, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer func() {
		if root != nil {
			_ = root.Close()
		}
	}()
	if err := root.Mkdir(taskID, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Lstat(taskID)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("task output path must be a real directory")
	}
	return nil
}

func ensureCollectionDirectory(rootDir, platform, collection string) error {
	if !filepath.IsAbs(rootDir) || !safeComponent(platform) || !safeComponent(collection) {
		return errors.New("invalid cloud collection path")
	}
	if err := os.MkdirAll(rootDir, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{platform, collection} {
		if err := root.Mkdir(name, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("cloud collection path must contain real directories")
		}
		next, err := root.OpenRoot(name)
		if err != nil {
			return err
		}
		_ = root.Close()
		root = next
	}
	return nil
}

func safeComponent(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.IsLocal(name) &&
		!strings.ContainsAny(name, `/\:*?"<>|`) && !strings.ContainsAny(name, "\x00\r\n") &&
		!strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ")
}

func randomID() string {
	var data [16]byte
	rand.Read(data[:])
	return hex.EncodeToString(data[:])
}
