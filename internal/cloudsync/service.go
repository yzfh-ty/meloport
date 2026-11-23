// Package cloudsync publishes complete local audio to a CloudDrive2 mount and verifies it on 115.
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
	TargetCID, MountDir, STRMDir, MediaBaseURL string
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

// Ready reports whether a file is already published or waiting for 115 verification.
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
	if filepath.Clean(file.TargetPath) != filepath.Clean(filepath.Join(s.opts.MountDir, filepath.FromSlash(file.RelativePath))) {
		if _, err := os.Lstat(file.LocalPath); err == nil {
			return false, nil // Re-publish an unfinished legacy intent to the new mount.
		}
	}
	if info, err := os.Lstat(file.TargetPath); err == nil {
		if !info.Mode().IsRegular() || info.Size() != file.Size {
			return false, errors.New("CloudDrive2 destination conflicts with the recorded audio; local file was preserved")
		}
		opened, err := os.Open(file.TargetPath)
		if err != nil {
			return false, err
		}
		digest := sha1.New()
		current, statErr := opened.Stat()
		n, readErr := io.Copy(digest, io.LimitReader(contextReader{ctx, opened}, file.Size+1))
		_ = opened.Close()
		if statErr != nil || !os.SameFile(info, current) || readErr != nil || n != file.Size || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), file.SHA1) {
			return false, errors.New("CloudDrive2 destination does not match the recorded audio; local file was preserved")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if _, localErr := os.Lstat(file.LocalPath); localErr == nil {
			return false, nil // Retry a copy interrupted before publication.
		} else if !errors.Is(localErr, os.ErrNotExist) {
			return false, errors.New("cannot inspect local audio for CloudDrive2 publication")
		}
		// Legacy move-based records may have lost both local paths after upload.
		// Preserve their remote verification and download suppression semantics.
		return true, nil
	} else {
		return false, errors.New("cannot inspect CloudDrive2 destination")
	}
	if err := s.store.MarkCloudPublished(ctx, file); err != nil {
		return false, err
	}
	return true, nil
}

// Stage copies verified tagged audio into the configured CloudDrive2 mount.
// Local downloads are retained; only a complete verified copy receives its final name.
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
		return false, errors.New("invalid 115 mount audio record")
	}
	if item.TagPending || item.TagFingerprint == "" {
		return false, errors.New("audio tags must finish before CloudDrive2 publication")
	}
	sha1sum, info, err := fingerprint(ctx, item)
	if err != nil {
		return false, err
	}
	folder := collection
	parts := []string{collection}
	if !legacyFolder {
		folder = filepath.Join(item.Provider, collection)
		parts = []string{item.Provider, collection}
	}
	if err := ensureDirectories(s.opts.MountDir, parts, false); err != nil {
		return false, fmt.Errorf("open CloudDrive2 mount; make sure the configured 115 target is mounted and writable: %w", err)
	}
	targetPath := filepath.Join(s.opts.MountDir, folder, name)
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return false, errors.New("CloudDrive2 mount already contains the audio filename; local file was preserved")
		}
		return false, errors.New("cannot inspect CloudDrive2 mount destination")
	}
	file, err := s.store.SaveCloudIntent(ctx, state.CloudFile{
		RootCID: s.opts.TargetCID, TaskID: item.TaskID, Provider: item.Provider, TrackID: item.TrackID,
		LocalPath: item.Path, TargetPath: targetPath, RelativePath: filepath.ToSlash(filepath.Join(folder, name)),
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
		return false, errors.New("local audio changed before CloudDrive2 publication")
	}
	if err := copyMounted(ctx, item, info, targetPath); err != nil {
		return false, fmt.Errorf("copy audio to CloudDrive2 mount; local file was preserved: %w", err)
	}
	return true, s.store.MarkCloudPublished(ctx, file)
}

func copyMounted(ctx context.Context, item state.Download, expected os.FileInfo, destination string) error {
	source, err := os.Open(item.Path)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !os.SameFile(expected, info) {
		return errors.New("local audio changed before copying")
	}
	root, err := os.OpenRoot(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer root.Close()
	temporary := ".meloport-" + randomID() + ".partial"
	output, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close(); _ = root.Remove(temporary) }()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(output, digest), io.LimitReader(contextReader{ctx, source}, item.Size+1))
	if err != nil {
		return err
	}
	if n != item.Size || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), item.SHA256) {
		return errors.New("copied audio does not match its download record")
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The temporary file and final name are on the destination filesystem, so
	// source and mount may be different volumes. Never overwrite existing audio.
	return renameNoReplace(filepath.Join(filepath.Dir(destination), temporary), destination)
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
		return "", nil, errors.New("local audio is missing or changed before CloudDrive2 publication")
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
	return ensureDirectories(rootDir, []string{taskID}, true)
}

func ensureCollectionDirectory(rootDir, platform, collection string) error {
	return ensureDirectories(rootDir, []string{platform, collection}, true)
}

func ensureDirectories(rootDir string, parts []string, createRoot bool) error {
	if !filepath.IsAbs(rootDir) {
		return errors.New("invalid cloud collection path")
	}
	for _, name := range parts {
		if !safeComponent(name) {
			return errors.New("invalid cloud collection path")
		}
	}
	if createRoot {
		if err := os.MkdirAll(rootDir, 0o750); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, name := range parts {
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
