package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meloport/internal/cloud115"
	"meloport/internal/cloudsync"
	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

type cloudTestEvents struct {
	serviceEventWriter
	mediaListening chan string
}

func (w cloudTestEvents) Write(data []byte) (int, error) {
	var event struct {
		Message string `json:"msg"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(data, &event); err == nil && event.Message == "115 media server listening" {
		select {
		case w.mediaListening <- event.Address:
		default:
		}
		return len(data), nil
	}
	return w.serviceEventWriter.Write(data)
}

func cloudServiceLogger() (*slog.Logger, cloudTestEvents) {
	events := cloudTestEvents{
		serviceEventWriter: serviceEventWriter{listening: make(chan string, 1), ready: make(chan struct{}, 1)},
		mediaListening:     make(chan string, 1),
	}
	return slog.New(slog.NewJSONHandler(events, nil)), events
}

type emptyCloudDirectory struct{ t *testing.T }

func (d emptyCloudDirectory) List(context.Context, string) ([]cloud115.File, error) {
	d.t.Error("115 requested a directory with no uploads pending")
	return nil, nil
}

func cloudServiceFixture(t *testing.T) (*config.Config, *state.Store, runner, serviceOptions) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("tasks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(root, "meloport.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &config.Config{
		Timezone: "UTC",
		Location: time.UTC,
		Server:   config.Server{Listen: "127.0.0.1:0"},
		Cloud115: config.Cloud115{Enabled: true, MediaEnabled: true, MediaListen: "127.0.0.1:0", PollInterval: config.Duration{Duration: time.Minute}},
	}
	service := cloudsync.New(store, emptyCloudDirectory{t}, cloudsync.Options{TargetCID: "12345"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /media/test", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://media.example.net/test.flac", http.StatusFound)
	})
	engine := serviceRunnerFunc(func(context.Context, model.Task) (syncer.Summary, error) {
		t.Error("unexpected task execution")
		return syncer.Summary{}, nil
	})
	return cfg, store, engine, serviceOptions{ConfigPath: configPath, Store: store, Cloud: &cloudRuntime{sync: service, handler: mux}}
}

func TestServeCloudListenersIsolateRoutesAndCloseOnShutdown(t *testing.T) {
	cfg, store, engine, options := cloudServiceFixture(t)
	logger, events := cloudServiceLogger()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = serve(ctx, cfg, nil, engine, store, logger, options)
	}()
	t.Cleanup(func() {
		cancel()
		waitServiceEvent(t, done)
	})
	managementAddr := waitServiceEvent(t, events.listening)
	mediaAddr := waitServiceEvent(t, events.mediaListening)
	waitServiceEvent(t, events.ready)

	client := &http.Client{
		Transport:     &http.Transport{},
		Timeout:       time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	for _, tc := range []struct {
		addr, path string
		status     int
		location   string
	}{
		{addr: managementAddr, path: "/readyz", status: http.StatusOK},
		{addr: managementAddr, path: "/api/overview", status: http.StatusUnauthorized},
		{addr: managementAddr, path: "/media/test", status: http.StatusNotFound},
		{addr: mediaAddr, path: "/readyz", status: http.StatusNotFound},
		{addr: mediaAddr, path: "/api/overview", status: http.StatusNotFound},
		{addr: mediaAddr, path: "/media/test", status: http.StatusFound, location: "https://media.example.net/test.flac"},
	} {
		resp, err := client.Get("http://" + tc.addr + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status || resp.Header.Get("Location") != tc.location {
			t.Errorf("%s%s: got status %d and location %q, want %d and %q", tc.addr, tc.path, resp.StatusCode, resp.Header.Get("Location"), tc.status, tc.location)
		}
	}
	client.CloseIdleConnections()
	cancel()
	waitServiceEvent(t, done)
	if serveErr != nil {
		t.Fatal(serveErr)
	}
	assertCloudAddressReleased(t, managementAddr)
	assertCloudAddressReleased(t, mediaAddr)
}

func TestServeMediaBindFailureClosesManagementListener(t *testing.T) {
	cfg, store, engine, options := cloudServiceFixture(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	cfg.Cloud115.MediaListen = occupied.Addr().String()
	logger, events := cloudServiceLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = serve(ctx, cfg, nil, engine, store, logger, options)
	if err == nil || !strings.Contains(err.Error(), "115 media playback") {
		t.Fatalf("expected the media bind failure, got %v", err)
	}
	assertCloudAddressReleased(t, waitServiceEvent(t, events.listening))
}

func TestServeCloudDoesNotBindDisabledMediaPort(t *testing.T) {
	cfg, store, engine, options := cloudServiceFixture(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	cfg.Cloud115.MediaEnabled = false
	cfg.Cloud115.MediaListen = occupied.Addr().String()
	logger, events := cloudServiceLogger()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = serve(ctx, cfg, nil, engine, store, logger, options)
	}()
	t.Cleanup(func() {
		cancel()
		waitServiceEvent(t, done)
	})
	managementAddr := waitServiceEvent(t, events.listening)
	waitServiceEvent(t, events.ready)
	select {
	case address := <-events.mediaListening:
		t.Fatalf("disabled media listener started on %s", address)
	default:
	}
	cancel()
	waitServiceEvent(t, done)
	if serveErr != nil {
		t.Fatal(serveErr)
	}
	assertCloudAddressReleased(t, managementAddr)
}

func TestCloudRuntimeOnlyActivatesExplicitNavidromeProxy(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "Navidrome fixture")
	}))
	t.Cleanup(upstream.Close)
	cfg := config.Cloud115{NavidromeURL: upstream.URL, NavidromeMountRoot: "/music"}
	runtime := newCloudRuntime(cfg, nil, nil)
	if runtime.handler != nil || runtime.sync != nil || runtime.login != nil {
		t.Fatal("saved endpoints must not activate disabled integrations")
	}
	cfg.MediaEnabled = true
	runtime = newCloudRuntime(cfg, nil, nil)
	response := httptest.NewRecorder()
	runtime.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/rest/ping", nil))
	if response.Code != http.StatusNotFound || requests.Load() != 0 {
		t.Fatal("an inactive Navidrome must not receive media requests")
	}
	cfg.NavidromeEnabled = true
	runtime = newCloudRuntime(cfg, nil, nil)
	response = httptest.NewRecorder()
	runtime.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/rest/stream?id=fixture", nil))
	if response.Code != http.StatusOK || response.Body.String() != "Navidrome fixture" || requests.Load() != 1 {
		t.Fatal("an explicitly enabled Navidrome should work without 115")
	}
	if runtime.sync != nil || runtime.login != nil {
		t.Fatal("a standalone Navidrome proxy must not activate cloud handoff")
	}
}

func TestCLIDefaultLocalModeIgnoresSavedMediaEndpoints(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("MELOPORT_115_ACCESS_TOKEN", "invalid-fixture-token")
	path := cliConfig(t, "cloud115:\n  media_listen: '"+occupied.Addr().String()+"'\n  navidrome_url: "+upstream.URL+"\n  navidrome_mount_root: /music\n")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var diagnostic strings.Builder
	if err := Run(ctx, []string{"serve", "-config", path}, io.Discard, &diagnostic, "test"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostic.String(), "115 media server listening") || requests.Load() != 0 {
		t.Fatal("local download mode activated an optional listener or upstream")
	}
	for _, name := range []string{"115-staging", "strm"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), "data", name)); !os.IsNotExist(err) {
			t.Fatalf("local mode created the optional %s directory: %v", name, err)
		}
	}
}

func assertCloudAddressReleased(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("service retained listener on %s: %v", address, err)
	}
	_ = listener.Close()
}

func TestCLICloudCommandsRejectDisabledOrMissingCredentialsBeforeOpeningState(t *testing.T) {
	t.Setenv("MELOPORT_115_TEST_ACCESS_TOKEN", "")
	for _, tc := range []struct {
		name, settings, diagnostic string
	}{
		{name: "disabled", diagnostic: "enable cloud115"},
		{
			name:       "missing token",
			settings:   "cloud115:\n  enabled: true\n  target_cid: '12345'\n  access_token_env: MELOPORT_115_TEST_ACCESS_TOKEN\n",
			diagnostic: "set the 115 access token in MELOPORT_115_TEST_ACCESS_TOKEN",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := cliConfig(t, tc.settings)
			for _, command := range []string{"cloud-check", "cloud-reconcile"} {
				err := Run(context.Background(), []string{command, "-config", path}, io.Discard, io.Discard, "test")
				if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
					t.Errorf("%s: expected %q, got %v", command, tc.diagnostic, err)
				}
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "data")); !os.IsNotExist(err) {
				t.Fatalf("credential errors should not open or create state, got %v", err)
			}
		})
	}
}
