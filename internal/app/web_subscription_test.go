package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"meloport/internal/chksz"
	"meloport/internal/download"
	"meloport/internal/model"
	"meloport/internal/provider"
	"meloport/internal/syncer"
)

// Exercise actual Web creation -> queue -> native metadata -> audio resolution
// -> download -> SQLite, then another manual subscription with no API key.
// All upstream responses are local fixtures.
func TestWebManualSubscriptionAutomaticallyDownloadsThenReusesLocalAudio(t *testing.T) {
	f := newWebFixture(t)
	var resolveCalls, audioCalls atomic.Int32
	payload, err := os.ReadFile(filepath.Join("..", "download", "testdata", "tone.flac"))
	if err != nil {
		t.Fatal(err)
	}
	var origin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/163_music":
			resolveCalls.Add(1)
			if r.URL.Query().Get("id") != "123" || r.URL.Query().Get("apikey") != webTestKey {
				t.Error("first download did not resolve the expected native ID")
			}
			fmt.Fprintf(w, `{"code":200,"url":%q,"size":%d}`, origin+"/audio.flac", len(payload))
		case "/audio.flac":
			audioCalls.Add(1)
			w.Header().Set("Content-Type", "audio/flac")
			_, _ = w.Write(payload)
		default:
			t.Errorf("unexpected upstream request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	origin = server.URL
	manager := provider.New(map[string]provider.Source{"netease": f.sources["netease"]}, chksz.NewLazy(chksz.Options{BaseURL: origin}, webTestKeyEnv), false, discardServiceLogger())
	dl, err := download.New(download.Options{})
	if err != nil {
		t.Fatal(err)
	}
	engine := syncer.NewWithSources(manager, manager, dl, f.store)
	outcomes := make(chan syncer.Summary, 2)
	cancel, done := startWebWorker(t, f, serviceRunnerFunc(func(ctx context.Context, task model.Task) (syncer.Summary, error) {
		summary, err := engine.Run(ctx, task)
		outcomes <- summary
		return summary, err
	}))
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/tasks", webTask("first", "netease")), http.StatusCreated)
	first := waitServiceEvent(t, outcomes)
	if first.Status != "success" || first.Downloaded != 1 || first.Skipped != 0 || resolveCalls.Load() != 1 || audioCalls.Load() != 1 {
		t.Fatalf("manual creation did not download automatically: %+v", first)
	}
	// Wait for the queue to finalize before deleting the first subscription.
	deadline := time.Now().Add(3 * time.Second)
	for f.queue.isRunning("first") {
		if time.Now().After(deadline) {
			t.Fatal("first download did not release the queue")
		}
		time.Sleep(time.Millisecond)
	}
	webStatus(t, webRequest(t, f, http.MethodDelete, "/api/tasks/first", nil), http.StatusOK)
	t.Setenv(webTestKeyEnv, "")
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/tasks", webTask("second", "netease")), http.StatusCreated)
	second := waitServiceEvent(t, outcomes)
	if second.Status != "success" || second.Downloaded != 0 || second.Skipped != 1 || resolveCalls.Load() != 1 || audioCalls.Load() != 1 || f.sources["netease"].playlists.Load() != 2 {
		t.Fatalf("new subscription did not reuse the deleted subscription's local audio without a key: %+v", second)
	}
	original, _, _ := f.store.GetDownload(t.Context(), "first", "netease", "123")
	reused, found, err := f.store.GetDownload(t.Context(), "second", "netease", "123")
	before, sourceErr := os.ReadFile(original.Path)
	after, targetErr := os.ReadFile(reused.Path)
	if err != nil || !found || sourceErr != nil || targetErr != nil || original.Path == reused.Path || !bytes.Equal(before, after) || reused.TagFingerprint == "" {
		t.Fatalf("reuse did not retain both subscription records and tagged audio: %+v, %v", reused, err)
	}
	cancel()
	waitServiceEvent(t, done)
}
