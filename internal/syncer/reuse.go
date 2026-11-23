package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"meloport/internal/model"
	"meloport/internal/state"
)

// reuseDownload copies verified local audio into this subscription's directory.
// Separate files keep later tag repairs and CloudDrive2 moves from affecting the source
// subscription. No audio resolution or network download is needed.
func (e *Engine) reuseDownload(ctx context.Context, task model.Task, track *model.Track, provider, directory, collection string, previous state.Download) (state.Download, bool, error) {
	candidates, err := e.store.FindDownloads(ctx, provider, track.ID)
	if err != nil {
		return state.Download{}, false, err
	}
	for _, candidate := range candidates {
		if !sameTrackVersion(*track, storedTrack(candidate)) {
			continue
		}
		result, found, err := copyLocalAudio(ctx, candidate, previous, directory, provider+"-"+track.ID)
		if err != nil {
			return state.Download{}, false, err
		}
		if !found {
			continue
		}
		fillStoredMetadata(track, storedTrack(candidate))
		item := candidate
		item.TaskID, item.Collection = task.ID, collection
		item.Path, item.Size, item.SHA256 = result.Path, result.Size, result.SHA256
		item.Title, item.Artists, item.Album = track.Name, track.Artists, track.Album
		item.Metadata, item.UpdatedAt = *track, time.Now().UTC()
		if err := e.store.SaveDownload(ctx, item); err != nil {
			return state.Download{}, false, err
		}
		return item, true, nil
	}
	return state.Download{}, false, nil
}

// Exact platform IDs are required by the database lookup. Conflicting available
// metadata additionally rejects a changed version; old records with incomplete
// metadata can still use the authoritative native ID.
func sameTrackVersion(track, stored model.Track) bool {
	for _, pair := range [][2]string{{track.Name, stored.Name}, {track.Album, stored.Album}, {track.Hash, stored.Hash}} {
		if pair[0] != "" && pair[1] != "" && normalizeTrackValue(pair[0]) != normalizeTrackValue(pair[1]) {
			return false
		}
	}
	if len(track.Artists) > 0 && len(stored.Artists) > 0 && !slices.Equal(artistNames(track.Artists), artistNames(stored.Artists)) {
		return false
	}
	if track.Duration > 0 && stored.Duration > 0 {
		difference := track.Duration - stored.Duration
		if difference < -3 || difference > 3 {
			return false
		}
	}
	return true
}

func normalizeTrackValue(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

func artistNames(artists []string) []string {
	var names []string
	for _, artist := range artists {
		for _, part := range strings.FieldsFunc(artist, func(r rune) bool { return r == '/' || r == '、' || r == '&' || r == ';' || r == '；' }) {
			if name := normalizeTrackValue(part); name != "" {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return slices.Compact(names)
}

func copyLocalAudio(ctx context.Context, source, previous state.Download, directory, basename string) (model.DownloadResult, bool, error) {
	var empty model.DownloadResult
	ext := strings.ToLower(filepath.Ext(source.Path))
	if !slices.Contains([]string{".mp3", ".flac", ".m4a", ".aac", ".ogg", ".opus", ".wav", ".aiff", ".ape", ".wv"}, ext) || !filepath.IsAbs(source.Path) {
		return empty, false, nil
	}
	if err := ctx.Err(); err != nil {
		return empty, false, err
	}
	info, err := os.Lstat(source.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != source.Size || source.Size <= 0 || source.Size == 1<<63-1 {
		return empty, false, nil
	}
	if digest, err := hex.DecodeString(source.SHA256); err != nil || len(digest) != sha256.Size {
		return empty, false, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return empty, false, errors.New("cannot create local audio reuse directory")
	}
	stage, err := os.CreateTemp(directory, "."+basename+"-reuse-*.part")
	if err != nil {
		return empty, false, errors.New("cannot stage local audio reuse")
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	if err := stage.Chmod(0o640); err != nil {
		return empty, false, errors.New("cannot set reused audio permissions")
	}
	matched, err := copyVerifiedAudio(ctx, source, stage)
	if err != nil || !matched {
		return empty, false, err
	}
	if err := stage.Sync(); err != nil {
		return empty, false, errors.New("cannot flush reused audio")
	}
	if err := stage.Close(); err != nil {
		return empty, false, errors.New("cannot close reused audio")
	}
	if err := ctx.Err(); err != nil {
		return empty, false, err
	}
	// A verified source is now safely staged. Preserve only a known truncated
	// destination before publishing; unknown existing files are never replaced.
	if err := quarantineIncompleteFile(previous, directory); err != nil {
		return empty, false, err
	}
	path := filepath.Join(directory, basename+ext)
	if err := os.Link(stage.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return empty, false, errors.New("cannot atomically publish reused audio (destination filesystem must support hard links)")
		}
		existing := source
		existing.Path = path
		matched, err := copyVerifiedAudio(ctx, existing, io.Discard)
		if err != nil {
			return empty, false, err
		}
		if !matched {
			return empty, false, errors.New("local audio reuse destination differs from the recorded song; preserved without changes")
		}
	}
	return model.DownloadResult{Path: path, Size: source.Size, SHA256: source.SHA256}, true, nil
}

// Hash while copying and verify the opened file and pathname still identify the
// original regular file. Missing, changed, symlinked or corrupt candidates are
// ignored so another local candidate can be tried before resolving audio.
func copyVerifiedAudio(ctx context.Context, source state.Download, target io.Writer) (bool, error) {
	info, err := os.Lstat(source.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != source.Size {
		return false, nil
	}
	file, err := os.Open(source.Path)
	if err != nil {
		return false, nil
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, nil
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(target, hash), io.LimitReader(localAudioReader{ctx, file}, source.Size+1))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, errors.New("cannot copy existing local audio")
	}
	current, err := os.Lstat(source.Path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
		return false, nil
	}
	return size == source.Size && strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), source.SHA256), nil
}

type localAudioReader struct {
	ctx context.Context
	io.Reader
}

func (r localAudioReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}
