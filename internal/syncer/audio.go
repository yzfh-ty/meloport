package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meloport/internal/download"
	"meloport/internal/model"
	"meloport/internal/state"
)

var (
	ErrAudioBusy        = errors.New("同步或补全任务正在运行，请稍后重试")
	ErrAudioNotFound    = errors.New("找不到这条音频记录")
	ErrAudioUnretryable = errors.New("当前文件状态无法补全，请刷新列表查看")
)

type AudioInspection struct {
	Status    string
	Missing   []string
	Retryable bool
	Title     string
	Artists   []string
	Album     string
}

type AudioRetagResult struct {
	Inspection AudioInspection
	Item       state.Download
	Warning    string
	Changed    bool
}

type audioInspector interface {
	InspectTags(context.Context, string) (download.AudioTags, error)
}

type trackMetadataReader interface {
	TrackMetadata(context.Context, string, model.Track) (model.Track, error)
}

func (e *Engine) InspectAudio(ctx context.Context, task model.Task, item state.Download) AudioInspection {
	track := storedTrack(item)
	view := AudioInspection{Status: "archived", Missing: []string{}, Title: track.Name, Artists: track.Artists, Album: track.Album}
	if task.ID == "" || task.ID != item.TaskID || taskPlatform(task) != item.Provider {
		return view
	}
	dir, err := filepath.Abs(task.Directory)
	if err != nil || !filepath.IsAbs(item.Path) || filepath.Clean(filepath.Dir(item.Path)) != filepath.Clean(dir) {
		view.Status = "outside"
		return view
	}
	info, err := os.Lstat(item.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		view.Status = "missing"
		return view
	case err != nil:
		view.Status = "unreadable"
		return view
	case !info.Mode().IsRegular() || info.Size() <= 0 || (info.Size() != item.Size && !item.TagPending):
		view.Status = "changed"
		return view
	}
	inspector, ok := e.downloader.(audioInspector)
	if !ok {
		view.Status = "unreadable"
		return view
	}
	tags, err := inspector.InspectTags(ctx, item.Path)
	if err != nil {
		view.Status = "unreadable"
		if errors.Is(err, download.ErrUnsupportedTags) {
			view.Status = "unsupported"
		}
		return view
	}
	if tags.Title != "" {
		view.Title = tags.Title
	} else {
		view.Missing = append(view.Missing, "title")
	}
	if len(tags.Artist) > 0 {
		view.Artists = tags.Artist
	} else {
		view.Missing = append(view.Missing, "artist")
	}
	if tags.Album != "" {
		view.Album = tags.Album
	} else {
		view.Missing = append(view.Missing, "album")
	}
	if !tags.Cover {
		view.Missing = append(view.Missing, "cover")
	}
	if !tags.Lyrics {
		view.Missing = append(view.Missing, "lyrics")
	}
	view.Status = "ready"
	if len(view.Missing) > 0 || item.TagPending {
		view.Status = "incomplete"
		_, view.Retryable = e.downloader.(Tagger)
	}
	return view
}

// RetagAudio shares the sync permit and the crash-safe write path with normal
// synchronization. A busy service gives immediate feedback to the caller.
func (e *Engine) RetagAudio(ctx context.Context, task model.Task, trackID string) (AudioRetagResult, error) {
	var result AudioRetagResult
	if !trackIDPattern.MatchString(trackID) {
		return result, ErrAudioNotFound
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	select {
	case e.permit <- struct{}{}:
		defer func() { <-e.permit }()
	default:
		return result, ErrAudioBusy
	}
	item, found, err := e.store.GetDownload(ctx, task.ID, taskPlatform(task), trackID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, ErrAudioNotFound
	}
	view := e.InspectAudio(ctx, task, item)
	result.Item, result.Inspection = item, view
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if view.Status == "ready" {
		return result, nil
	}
	if !view.Retryable {
		return result, ErrAudioUnretryable
	}
	original, err := os.Lstat(item.Path)
	if err != nil || !original.Mode().IsRegular() || original.Size() <= 0 || (original.Size() != item.Size && !item.TagPending) {
		return result, ErrAudioUnretryable
	}
	track := storedTrack(item)
	if reader, ok := e.reader.(trackMetadataReader); ok && needsNativeMetadata(view.Missing, track) {
		lookup, cancel := context.WithTimeout(ctx, 20*time.Second)
		fresh, err := reader.TrackMetadata(lookup, item.Provider, track)
		cancel()
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err != nil {
			result.Warning = "平台歌曲资料暂时无法获取"
		} else {
			// Prefer existing recorded values and only supply fields still empty.
			fresh.CoverURL = model.CanonicalCoverURL(fresh.CoverURL)
			fillStoredMetadata(&track, fresh)
		}
	}
	// Native requests can take time. Recheck that the record still covers a
	// regular file before entering the existing staged replacement flow.
	current, err := os.Lstat(item.Path)
	if err != nil || !os.SameFile(original, current) || current.Size() != original.Size() || !current.ModTime().Equal(original.ModTime()) {
		return result, ErrAudioUnretryable
	}
	changed, warning, err := e.tagExisting(ctx, item, track, true)
	if err != nil {
		return result, err
	}
	item, found, err = e.store.GetDownload(ctx, task.ID, item.Provider, trackID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, ErrAudioNotFound
	}
	result.Changed, result.Item = changed, item
	result.Inspection = e.InspectAudio(ctx, task, item)
	if warning != "" {
		if result.Warning != "" {
			result.Warning += "。"
		}
		result.Warning += warning
	}
	return result, ctx.Err()
}

func taskPlatform(task model.Task) string {
	if task.Platform == "" {
		return "netease"
	}
	return task.Platform
}

func needsNativeMetadata(missing []string, track model.Track) bool {
	for _, field := range missing {
		switch field {
		case "title":
			if strings.TrimSpace(track.Name) == "" {
				return true
			}
		case "artist":
			if len(track.Artists) == 0 {
				return true
			}
		case "album":
			if strings.TrimSpace(track.Album) == "" {
				return true
			}
		case "cover":
			if track.CoverURL == "" {
				return true
			}
		case "lyrics":
			// Older Kugou records can have a numeric audio ID instead of a hash.
			if track.Platform == "kugou" && track.Hash == "" && len(track.ID) != 32 {
				return true
			}
		}
	}
	return false
}
