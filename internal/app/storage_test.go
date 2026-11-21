package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/syncer"
)

func musicSettings(root *string) config.SettingsInput {
	return config.SettingsInput{Timezone: "Asia/Shanghai", Schedule: config.DefaultSchedule, Quality: "lossless", FallbackToAPI: true, MusicDir: root}
}

func TestWebMusicDirectorySettingsPersistAndPendingTasksUseNewRoot(t *testing.T) {
	f := newWebFixture(t, webTask("queued", "netease"))
	oldDirectory := f.cfg.Tasks[0].Directory
	oldAudio := filepath.Join(oldDirectory, "saved.flac")
	if err := os.MkdirAll(oldDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldAudio, []byte("saved audio"), 0600); err != nil {
		t.Fatal(err)
	}
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/tasks/queued/run", map[string]any{}), http.StatusAccepted)
	root := "downloads/library"
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", musicSettings(&root)), http.StatusOK)
	resolved := filepath.Join(filepath.Dir(f.path), "downloads", "library")
	var overview struct {
		MusicDir         string `json:"music_dir"`
		MusicDirResolved string `json:"music_dir_resolved"`
	}
	w := webRequest(t, f, http.MethodGet, "/api/overview", nil)
	webStatus(t, w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.MusicDir != root || overview.MusicDirResolved != resolved {
		t.Fatalf("overview did not expose configured/resolved roots: %+v", overview)
	}
	cfg, err := config.Load(f.path)
	if err != nil || cfg.Storage.MusicDirConfigured != root || cfg.Storage.MusicDir != resolved || cfg.Tasks[0].Directory != filepath.Join(resolved, "queued") {
		t.Fatalf("stored root was not reloaded correctly: %+v %v", cfg, err)
	}
	if _, err := os.Stat(resolved); !os.IsNotExist(err) {
		t.Fatalf("saving settings created the target directory: %v", err)
	}
	if data, err := os.ReadFile(oldAudio); err != nil || string(data) != "saved audio" {
		t.Fatalf("existing audio changed during root update: %q %v", data, err)
	}
	executed := make(chan model.Task, 1)
	engine := serviceRunnerFunc(func(ctx context.Context, task model.Task) (syncer.Summary, error) {
		executed <- task
		return syncer.Summary{Status: "success"}, nil
	})
	cancel, done := startWebWorker(t, f, engine)
	got := waitServiceEvent(t, executed)
	if got.Directory != filepath.Join(resolved, "queued") {
		t.Fatalf("pending sync retained stale root: %+v", got)
	}
	cancel()
	waitServiceEvent(t, done)
}

func TestWebMusicDirectoryChangeRefusedWhileSyncIsRunning(t *testing.T) {
	f := newWebFixture(t, webTask("running", "netease"))
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/tasks/running/run", map[string]any{}), http.StatusAccepted)
	started := make(chan struct{}, 1)
	engine := serviceRunnerFunc(func(ctx context.Context, task model.Task) (syncer.Summary, error) {
		started <- struct{}{}
		<-ctx.Done()
		return syncer.Summary{Status: "canceled"}, ctx.Err()
	})
	cancel, done := startWebWorker(t, f, engine)
	waitServiceEvent(t, started)
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	root := "different-output"
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", musicSettings(&root)), http.StatusConflict)
	after, err := os.ReadFile(f.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("busy root change modified persisted settings")
	}
	f.control.mu.RLock()
	current := f.control.cfg.Storage.MusicDir
	f.control.mu.RUnlock()
	if current != f.cfg.Storage.MusicDir {
		t.Fatal("busy root change altered active configuration")
	}
	// Full settings forms may send the unchanged root; old clients may omit it.
	// Both remain safe while a sync is using that same resolved location.
	sameRoot := "data/music"
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", musicSettings(&sameRoot)), http.StatusOK)
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", musicSettings(nil)), http.StatusOK)
	cancel()
	waitServiceEvent(t, done)
}

func TestWebMusicDirectoryValidationAndWriteGuards(t *testing.T) {
	f := newWebFixture(t)
	file := filepath.Join(filepath.Dir(f.path), "existing.flac")
	if err := os.WriteFile(file, []byte("original file"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(f.path)
	for _, root := range []string{"", " \t ", "invalid\x00path", file, filepath.Join(file, "child")} {
		webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", musicSettings(&root)), http.StatusBadRequest)
	}
	root := "new-music"
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", musicSettings(&root), func(r *http.Request) { r.Header.Del("X-Meloport-Request") }), http.StatusForbidden)
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", musicSettings(&root), func(r *http.Request) { r.Header.Set("Origin", "http://foreign.example") }), http.StatusForbidden)
	after, _ := os.ReadFile(f.path)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid/untrusted root setting changed configuration")
	}
}
