package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

type audioLibraryFixture struct {
	inspect func(context.Context, model.Task, state.Download) syncer.AudioInspection
	retag   func(context.Context, model.Task, string) (syncer.AudioRetagResult, error)
}

func (a audioLibraryFixture) InspectAudio(ctx context.Context, task model.Task, item state.Download) syncer.AudioInspection {
	return a.inspect(ctx, task, item)
}

func (a audioLibraryFixture) RetagAudio(ctx context.Context, task model.Task, trackID string) (syncer.AudioRetagResult, error) {
	return a.retag(ctx, task, trackID)
}

func TestAudioLibraryFiltersAndBoundedInspection(t *testing.T) {
	f := newWebFixture(t, webTask("library", "netease"), webTask("empty", "netease"))
	for i := 0; i <= audioScanLimit; i++ {
		id := fmt.Sprintf("%03d", i+1)
		item := state.Download{TaskID: "library", Provider: "netease", TrackID: id, Title: "山河", Size: 1, Path: filepath.Join(t.TempDir(), id+".mp3")}
		if err := f.store.SaveDownload(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []state.Download{
		{TaskID: "library", Provider: "netease", TrackID: "999", Title: "其他歌曲", Path: "other.mp3", Size: 1},
		{TaskID: "removed", Provider: "qq", TrackID: "001", Title: "山河", Path: "archived.mp3", Size: 1},
	} {
		if err := f.store.SaveDownload(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	inspections := 0
	f.control.audio = audioLibraryFixture{inspect: func(_ context.Context, task model.Task, item state.Download) syncer.AudioInspection {
		inspections++
		view := syncer.AudioInspection{Status: "ready", Title: item.Title}
		switch {
		case task.ID == "":
			view.Status = "archived"
		case item.TrackID == fmt.Sprintf("%03d", audioScanLimit+1):
			view.Status, view.Missing, view.Retryable = "incomplete", []string{"lyrics"}, true
		}
		return view
	}}
	webStatus(t, webRequest(t, f, http.MethodGet, "/api/audio", nil, func(r *http.Request) {
		r.Header.Del("X-Meloport-Request")
	}), http.StatusForbidden)
	get := func(path string) (page struct {
		Items      []audioView `json:"items"`
		Tasks      []string    `json:"tasks"`
		HasMore    bool        `json:"has_more"`
		NextOffset int         `json:"next_offset"`
	}) {
		w := webRequest(t, f, http.MethodGet, path, nil)
		webStatus(t, w, http.StatusOK)
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	base := "/api/audio?task=library&platform=netease&q=%E5%B1%B1%E6%B2%B3&missing=lyrics"
	first := get(base)
	if first.Items == nil || len(first.Items) != 0 || !first.HasMore || first.NextOffset != audioScanLimit || inspections != audioScanLimit {
		t.Fatalf("first page did not advance a bounded batch: %+v (inspected %d)", first, inspections)
	}
	if !reflect.DeepEqual(first.Tasks, []string{"empty", "library", "removed"}) {
		t.Fatalf("unexpected task filters: %v", first.Tasks)
	}
	second := get(fmt.Sprintf("%s&offset=%d", base, first.NextOffset))
	if second.HasMore || len(second.Items) != 1 || second.Items[0].TrackID != fmt.Sprintf("%03d", audioScanLimit+1) || !second.Items[0].Retryable || inspections != audioScanLimit+1 {
		t.Fatalf("unexpected second page: %+v (inspected %d)", second, inspections)
	}
	archived := get("/api/audio?task=removed&platform=qq")
	if len(archived.Items) != 1 || archived.Items[0].Status != "archived" || archived.Items[0].Retryable {
		t.Fatalf("unexpected archived entry: %+v", archived)
	}
}

func TestAudioRetagGuardsTaskAndStorageWhileRepairing(t *testing.T) {
	disabled := webTask("library", "netease")
	disabled.Enabled = false
	f := newWebFixture(t, disabled)
	const path = "/api/audio/library/netease/123/retag"
	entered := make(chan model.Task, 1)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.control.audio = audioLibraryFixture{retag: func(ctx context.Context, task model.Task, trackID string) (syncer.AudioRetagResult, error) {
		if trackID != "123" {
			return syncer.AudioRetagResult{}, syncer.ErrAudioNotFound
		}
		entered <- task
		select {
		case <-release:
			return syncer.AudioRetagResult{
				Changed: true, Warning: "封面暂时无法下载",
				Item:       state.Download{TaskID: task.ID, Provider: task.Platform, TrackID: trackID, Path: filepath.Join(task.Directory, "netease-123.flac"), Size: 2048},
				Inspection: syncer.AudioInspection{Status: "incomplete", Title: "歌曲", Missing: []string{"cover"}, Retryable: true},
			}, nil
		case <-ctx.Done():
			return syncer.AudioRetagResult{}, ctx.Err()
		}
	}}
	webStatus(t, webRequest(t, f, http.MethodPost, path, struct{}{}, func(r *http.Request) {
		r.Header.Set("Origin", "http://other.invalid")
	}), http.StatusForbidden)
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/audio/library/qq/123/retag", struct{}{}), http.StatusConflict)
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://localhost:8080"+path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Meloport-Request", "1")
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.handler.ServeHTTP(w, r)
	}()
	select {
	case task := <-entered:
		if task.ID != disabled.ID || task.Platform != disabled.Platform || task.Directory == "" {
			t.Fatalf("retag received wrong current task: %+v", task)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repair did not start")
	}
	webStatus(t, webRequest(t, f, http.MethodPost, path, struct{}{}), http.StatusConflict)
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/tasks/library", disabled), http.StatusConflict)
	webStatus(t, webRequest(t, f, http.MethodDelete, "/api/tasks/library", nil), http.StatusConflict)
	root := filepath.Join(t.TempDir(), "another-root")
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", config.SettingsInput{MusicDir: &root}), http.StatusConflict)
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("repair did not finish")
	}
	webStatus(t, w, http.StatusOK)
	var response struct {
		Item     audioView `json:"item"`
		Warnings []string  `json:"warnings"`
		Changed  bool      `json:"changed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !response.Changed || response.Item.Size != 2048 || !reflect.DeepEqual(response.Warnings, []string{"封面暂时无法下载"}) {
		t.Fatalf("unexpected repair result: %+v %v", response, err)
	}
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/audio/library/netease/456/retag", struct{}{}), http.StatusNotFound)
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/tasks/library", disabled), http.StatusOK)
}
