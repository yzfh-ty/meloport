// Package syncer downloads missing playlist tracks and records durable outcomes.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"meloport/internal/chksz"
	"meloport/internal/model"
	"meloport/internal/state"
)

var trackIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type Provider interface {
	Playlist(context.Context, string) (model.Playlist, error)
	Resolve(context.Context, string, string) (model.ResolvedTrack, error)
}

type Downloader interface {
	Download(context.Context, model.ResolvedTrack, string, string) (model.DownloadResult, error)
}

type PlaylistReader interface {
	Playlist(context.Context, string, string) (model.Playlist, error)
}

type typedPlaylistReader interface {
	PlaylistFor(context.Context, string, string, string) (model.Playlist, error)
}

type AudioResolver interface {
	Resolve(context.Context, string, model.Track, string) (model.ResolvedTrack, error)
}

type legacyProvider struct{ Provider }

func (p legacyProvider) Playlist(ctx context.Context, source, id string) (model.Playlist, error) {
	if source != "netease" {
		return model.Playlist{}, errors.New("legacy provider only supports netease")
	}
	return p.Provider.Playlist(ctx, id)
}

func (p legacyProvider) PlaylistFor(ctx context.Context, source, sourceType, id string) (model.Playlist, error) {
	if sourceType != "" && sourceType != "playlist" {
		return model.Playlist{}, errors.New("legacy provider does not support ranking sources")
	}
	return p.Playlist(ctx, source, id)
}

func (p legacyProvider) Resolve(ctx context.Context, source string, track model.Track, quality string) (model.ResolvedTrack, error) {
	if source != "netease" {
		return model.ResolvedTrack{}, errors.New("legacy provider only supports netease")
	}
	return p.Provider.Resolve(ctx, track.ID, quality)
}

type Engine struct {
	reader     PlaylistReader
	resolver   AudioResolver
	downloader Downloader
	store      *state.Store
	permit     chan struct{}
}

type Summary struct {
	RunID          int64     `json:"run_id"`
	TaskID         string    `json:"task_id"`
	Status         string    `json:"status"`
	PlaylistName   string    `json:"playlist_name"`
	MetadataSource string    `json:"metadata_source,omitempty"`
	Total          int       `json:"total"`
	ReportedCount  int       `json:"reported_count"`
	Downloaded     int       `json:"downloaded"`
	Skipped        int       `json:"skipped"`
	Failed         int       `json:"failed"`
	Pending        int       `json:"pending"`
	Error          string    `json:"error,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
}

func New(provider Provider, downloader Downloader, store *state.Store) *Engine {
	adapter := legacyProvider{provider}
	return NewWithSources(adapter, adapter, downloader, store)
}

func NewWithSources(reader PlaylistReader, resolver AudioResolver, downloader Downloader, store *state.Store) *Engine {
	return &Engine{reader: reader, resolver: resolver, downloader: downloader, store: store, permit: make(chan struct{}, 1)}
}

// Run serializes all tasks sharing this engine. Waiting is canceled on shutdown.
func (e *Engine) Run(ctx context.Context, task model.Task) (summary Summary, returnedErr error) {
	summary.TaskID = task.ID
	providerName := task.Platform
	if providerName == "" {
		providerName = "netease"
	}
	if providerName != "netease" && providerName != "qq" && providerName != "kugou" {
		summary.Status = "failed"
		return summary, errors.New("unsupported task platform")
	}
	select {
	case e.permit <- struct{}{}:
		defer func() { <-e.permit }()
	case <-ctx.Done():
		summary.Status = "canceled"
		return summary, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		summary.Status = "canceled"
		return summary, err
	}
	summary.StartedAt = time.Now().UTC()
	summary.Status = "running"
	var err error
	summary.RunID, err = e.store.StartRun(ctx, task.ID)
	if err != nil {
		summary.Status = "failed"
		return summary, err
	}
	defer func() {
		summary.FinishedAt = time.Now().UTC()
		if returnedErr != nil {
			summary.Error = bounded(returnedErr.Error(), 4096)
		}
		if errors.Is(returnedErr, context.Canceled) || errors.Is(returnedErr, context.DeadlineExceeded) || ctx.Err() != nil {
			summary.Status = "canceled"
		} else if summary.Status == "running" {
			if returnedErr != nil {
				summary.Status = "failed"
			} else {
				summary.Status = "success"
			}
		}
		// A canceled task still needs a durable final outcome.
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		finished := summary.FinishedAt
		if err := e.store.FinishRun(finishCtx, state.Run{
			ID: summary.RunID, TaskID: summary.TaskID, Status: summary.Status,
			FinishedAt: &finished, PlaylistName: summary.PlaylistName, Total: summary.Total,
			ReportedCount: summary.ReportedCount, Downloaded: summary.Downloaded,
			Skipped: summary.Skipped, Failed: summary.Failed, Pending: summary.Pending, Error: summary.Error,
		}); err != nil {
			returnedErr = errors.Join(returnedErr, err)
			summary.Status = "failed"
			summary.Error = bounded(returnedErr.Error(), 4096)
		}
	}()
	directory, err := filepath.Abs(task.Directory)
	if err != nil {
		return summary, fmt.Errorf("resolve task output directory: %w", err)
	}
	sourceType := task.SourceType
	if sourceType == "" {
		sourceType = "playlist"
	}
	sourceID := task.SourceID
	if sourceID == "" {
		sourceID = task.PlaylistID
	}
	var playlist model.Playlist
	if typed, ok := e.reader.(typedPlaylistReader); ok {
		playlist, err = typed.PlaylistFor(ctx, providerName, sourceType, sourceID)
	} else if sourceType == "playlist" {
		playlist, err = e.reader.Playlist(ctx, providerName, sourceID)
	} else {
		err = errors.New("ranking source requires a typed playlist reader")
	}
	if err != nil {
		return summary, fmt.Errorf("read playlist: %w", err)
	}
	summary.PlaylistName = playlist.Name
	summary.MetadataSource = playlist.FetchedVia
	summary.ReportedCount = playlist.ReportedCount
	tracks := uniqueTracks(playlist.Tracks)
	summary.Total = len(tracks)
	summary.Pending = len(tracks)
	issues := make([]string, 0, 6)
	issueCount := 0
	addIssue := func(message string) {
		issueCount++
		if len(issues) < 5 {
			issues = append(issues, bounded(message, 600))
		}
	}
	if playlist.ReportedCount > len(playlist.Tracks) {
		addIssue(fmt.Sprintf("playlist is incomplete: expected %d tracks, received %d", playlist.ReportedCount, len(playlist.Tracks)))
	}
	for _, track := range tracks {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if !trackIDPattern.MatchString(track.ID) {
			summary.Failed++
			summary.Pending--
			addIssue("playlist contains an invalid track ID")
			continue
		}
		stored, found, err := e.store.GetDownload(ctx, task.ID, providerName, track.ID)
		if err != nil {
			return summary, err
		}
		if found && completeFile(stored, directory) {
			summary.Skipped++
			summary.Pending--
			continue
		}
		audio, err := e.resolver.Resolve(ctx, providerName, track, task.Quality)
		if err == nil && found {
			err = quarantineIncompleteFile(stored, directory)
		}
		if err == nil {
			var result model.DownloadResult
			result, err = e.downloader.Download(ctx, audio, directory, providerName+"-"+track.ID)
			if err == nil {
				err = e.store.SaveDownload(ctx, state.Download{
					TaskID: task.ID, Provider: providerName, TrackID: track.ID,
					Path: result.Path, Size: result.Size, SHA256: result.SHA256,
					Title: track.Name, Artists: track.Artists, Album: track.Album,
				})
				if err != nil {
					return summary, err
				}
				summary.Downloaded++
				summary.Pending--
				continue
			}
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		summary.Failed++
		summary.Pending--
		addIssue(fmt.Sprintf("track %s: %v", track.ID, err))
		var apiError *chksz.APIError
		if chksz.IsAccountError(err) || (errors.As(err, &apiError) && apiError.Status == 503) {
			summary.Status = "failed"
			return summary, fmt.Errorf("track %s: %w", track.ID, err)
		}
	}
	if issueCount > 0 {
		summary.Status = "partial"
		if summary.Downloaded+summary.Skipped == 0 {
			summary.Status = "failed"
		}
		if issueCount > len(issues) {
			issues = append(issues, fmt.Sprintf("and %d more errors", issueCount-len(issues)))
		}
		return summary, errors.New(strings.Join(issues, "; "))
	}
	return summary, nil
}

func uniqueTracks(tracks []model.Track) []model.Track {
	seen := make(map[string]struct{}, len(tracks))
	unique := make([]model.Track, 0, len(tracks))
	for _, track := range tracks {
		if _, exists := seen[track.ID]; exists {
			continue
		}
		seen[track.ID] = struct{}{}
		unique = append(unique, track)
	}
	return unique
}

func completeFile(item state.Download, directory string) bool {
	if item.Size <= 0 || !filepath.IsAbs(item.Path) || filepath.Clean(filepath.Dir(item.Path)) != filepath.Clean(directory) {
		return false
	}
	info, err := os.Lstat(item.Path)
	return err == nil && info.Mode().IsRegular() && info.Size() == item.Size
}

// Preserve a known incomplete tracked file before asking the downloader to
// recreate it. The downloader can then safely adopt unrelated existing files.
func quarantineIncompleteFile(item state.Download, directory string) error {
	if !filepath.IsAbs(item.Path) || filepath.Clean(filepath.Dir(item.Path)) != filepath.Clean(directory) {
		return nil
	}
	info, err := os.Lstat(item.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == item.Size {
		return nil
	}
	backup, err := os.CreateTemp(directory, filepath.Base(item.Path)+".corrupt-*")
	if err != nil {
		return fmt.Errorf("reserve incomplete-file backup: %w", err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		os.Remove(backupPath)
		return fmt.Errorf("close incomplete-file backup: %w", err)
	}
	if err := os.Rename(item.Path, backupPath); err != nil {
		os.Remove(backupPath)
		return fmt.Errorf("preserve incomplete file: %w", err)
	}
	return nil
}

func bounded(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
