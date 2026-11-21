package app

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

type audioLibrary interface {
	InspectAudio(context.Context, model.Task, state.Download) syncer.AudioInspection
	RetagAudio(context.Context, model.Task, string) (syncer.AudioRetagResult, error)
}

type audioView struct {
	TaskID    string   `json:"task_id"`
	Provider  string   `json:"provider"`
	TrackID   string   `json:"track_id"`
	Path      string   `json:"path"`
	Size      int64    `json:"size"`
	Title     string   `json:"title"`
	Artists   []string `json:"artists"`
	Album     string   `json:"album"`
	Missing   []string `json:"missing"`
	Status    string   `json:"status"`
	Retryable bool     `json:"retryable"`
}

func audioItem(item state.Download, inspection syncer.AudioInspection) audioView {
	return audioView{
		TaskID: item.TaskID, Provider: item.Provider, TrackID: item.TrackID,
		Path: item.Path, Size: item.Size, Title: inspection.Title,
		Artists: append([]string{}, inspection.Artists...), Album: inspection.Album,
		Missing: append([]string{}, inspection.Missing...), Status: inspection.Status, Retryable: inspection.Retryable,
	}
}

func audioPlatform(value string) bool {
	return value == "netease" || value == "qq" || value == "kugou"
}

func audioMatches(inspection syncer.AudioInspection, missing string) bool {
	switch missing {
	case "":
		return true
	case "none":
		return inspection.Status == "ready"
	case "any":
		return len(inspection.Missing) > 0
	default:
		return slices.Contains(inspection.Missing, missing)
	}
}

const audioScanLimit = 120

func (c *webController) listAudio(w http.ResponseWriter, r *http.Request) {
	if c.store == nil || c.audio == nil {
		webError(w, http.StatusServiceUnavailable, "audio management is unavailable")
		return
	}
	query := r.URL.Query()
	taskID, platform := query.Get("task"), query.Get("platform")
	keyword := strings.TrimSpace(query.Get("q"))
	missing := query.Get("missing")
	switch missing {
	case "", "any", "none", "title", "artist", "album", "cover", "lyrics":
	default:
		webError(w, http.StatusBadRequest, "unknown audio metadata filter")
		return
	}
	if (platform != "" && !audioPlatform(platform)) || len(taskID) > 128 || utf8.RuneCountInString(keyword) > 200 {
		webError(w, http.StatusBadRequest, "invalid audio task, platform or search keyword")
		return
	}
	limit, offset := 30, 0
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			webError(w, http.StatusBadRequest, "audio limit must be between 1 and 50")
			return
		}
		limit = value
	}
	if raw := query.Get("offset"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || value < 0 {
			webError(w, http.StatusBadRequest, "invalid audio offset")
			return
		}
		offset = int(value)
	}
	c.mu.RLock()
	tasks := c.tasks // install replaces this map in full, so the snapshot stays immutable.
	c.mu.RUnlock()
	ctx := r.Context()
	ids, err := c.store.AudioTaskIDs(ctx)
	if err != nil {
		webError(w, http.StatusInternalServerError, "cannot read audio task filters")
		return
	}
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	for id := range tasks {
		if !known[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	// Keep a lookahead row without inspecting it. This cursor counts database
	// rows, so a selective tag filter can return fewer results in one batch.
	records, err := c.store.SearchAudio(ctx, state.AudioSearch{
		TaskID: taskID, Provider: platform, Keyword: keyword, Offset: offset, Limit: audioScanLimit + 1,
	})
	if err != nil {
		webError(w, http.StatusInternalServerError, "cannot read the audio library")
		return
	}
	items := make([]audioView, 0, limit)
	scanned := 0
	for _, record := range records {
		if scanned == audioScanLimit || len(items) == limit {
			break
		}
		if ctx.Err() != nil {
			return
		}
		task := tasks[record.TaskID].Model()
		if task.Platform != record.Provider {
			task = model.Task{}
		}
		inspection := c.audio.InspectAudio(ctx, task, record)
		if audioMatches(inspection, missing) {
			items = append(items, audioItem(record, inspection))
		}
		scanned++
	}
	webJSON(w, http.StatusOK, map[string]any{
		"items": items, "tasks": append([]string{}, ids...), "has_more": scanned < len(records), "next_offset": offset + scanned,
	})
}

func (c *webController) retagAudio(w http.ResponseWriter, r *http.Request) {
	if c.store == nil || c.audio == nil {
		webError(w, http.StatusServiceUnavailable, "audio management is unavailable")
		return
	}
	taskID, platform, trackID := r.PathValue("task"), r.PathValue("platform"), r.PathValue("track")
	if !audioPlatform(platform) || len(trackID) > 128 {
		webError(w, http.StatusBadRequest, "invalid audio platform or track ID")
		return
	}
	if !readWebJSON(w, r, &struct{}{}) {
		return
	}
	c.changes.Lock()
	c.mu.RLock()
	task, found := c.tasks[taskID]
	stopping := c.stopping
	c.mu.RUnlock()
	if stopping || !found || task.Platform != platform || c.audioActiveTask != "" || c.queue.isRunning(taskID) {
		c.changes.Unlock()
		switch {
		case stopping:
			webError(w, http.StatusServiceUnavailable, "service is stopping")
		case !found || task.Platform != platform:
			webError(w, http.StatusConflict, "audio does not belong to a configured task for this platform")
		default:
			webError(w, http.StatusConflict, "a sync or repair is running; try again after it finishes")
		}
		return
	}
	c.audioActiveTask = taskID
	c.changes.Unlock()
	defer func() {
		c.changes.Lock()
		c.audioActiveTask = ""
		c.changes.Unlock()
	}()
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	result, err := c.audio.RetagAudio(ctx, task.Model(), trackID)
	switch {
	case errors.Is(err, syncer.ErrAudioBusy):
		webError(w, http.StatusConflict, "a sync or repair is running; try again after it finishes")
	case errors.Is(err, syncer.ErrAudioNotFound):
		webError(w, http.StatusNotFound, "audio record not found")
	case errors.Is(err, syncer.ErrAudioUnretryable):
		webError(w, http.StatusConflict, "audio is unavailable for metadata repair; refresh the list")
	case errors.Is(err, context.DeadlineExceeded):
		webError(w, http.StatusGatewayTimeout, "audio metadata repair timed out")
	case errors.Is(err, context.Canceled):
		webError(w, http.StatusServiceUnavailable, "audio metadata repair was interrupted")
	case err != nil:
		webError(w, http.StatusInternalServerError, "cannot repair audio metadata")
	default:
		warnings := []string{}
		if result.Warning != "" {
			warnings = append(warnings, result.Warning)
		}
		webJSON(w, http.StatusOK, map[string]any{
			"item": audioItem(result.Item, result.Inspection), "warnings": warnings, "changed": result.Changed,
		})
	}
}
