package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/provider"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

const webTestKeyEnv = "MELOPORT_WEB_TEST_API_KEY"
const webTestKey = "chksz_web_fixture_private_not_real"
const webTestCookie = "MUSIC_U=web_fixture_cookie_not_real"

type webTestSource struct {
	playlists, searches atomic.Int32
	rankings            atomic.Int32
	rankingReads        atomic.Int32
	fail                atomic.Bool
}

func (s *webTestSource) Playlist(ctx context.Context, id string) (model.Playlist, error) {
	s.playlists.Add(1)
	if s.fail.Load() {
		return model.Playlist{}, errors.New("native fixture unavailable")
	}
	return model.Playlist{ID: id, Name: "Native list", ReportedCount: 1, Tracks: []model.Track{{ID: "123", Name: "Native song", Artists: []string{"Singer"}}}}, nil
}

func (s *webTestSource) Search(ctx context.Context, keyword string, limit, offset int) ([]model.Track, error) {
	s.searches.Add(1)
	if s.fail.Load() {
		return nil, errors.New("native fixture unavailable")
	}
	return []model.Track{{ID: "123", Name: keyword, Artists: []string{"Singer"}}}, nil
}

func (s *webTestSource) Rankings(context.Context) ([]model.Ranking, error) {
	s.rankings.Add(1)
	if s.fail.Load() {
		return nil, errors.New("native fixture unavailable")
	}
	return []model.Ranking{{ID: "19723756", Name: "Native chart", Category: "流行", TrackCount: 10, UpdateFrequency: "每日更新"}}, nil
}

func (s *webTestSource) Ranking(ctx context.Context, id string) (model.Playlist, error) {
	s.rankingReads.Add(1)
	if s.fail.Load() {
		return model.Playlist{}, errors.New("native fixture unavailable")
	}
	return model.Playlist{ID: id, Name: "Native chart", ReportedCount: 1, Tracks: []model.Track{{ID: "123", Name: "Chart song", Artists: []string{"Singer"}}}}, nil
}

type webTestGateway struct{ calls atomic.Int32 }

func (g *webTestGateway) Playlist(context.Context, string) (model.Playlist, error) {
	g.calls.Add(1)
	return model.Playlist{}, errors.New("unexpected metered playlist")
}
func (g *webTestGateway) Search(context.Context, string, string, int, int) ([]model.Track, error) {
	g.calls.Add(1)
	return nil, errors.New("unexpected metered search")
}
func (g *webTestGateway) ResolvePlatform(context.Context, string, string, string) (model.ResolvedTrack, error) {
	g.calls.Add(1)
	return model.ResolvedTrack{}, errors.New("unexpected metered audio")
}

type webFixture struct {
	path     string
	cfg      *config.Config
	store    *state.Store
	control  *webController
	queue    *taskQueue
	ready    atomic.Bool
	handler  http.Handler
	sources  map[string]*webTestSource
	gateway  *webTestGateway
	metadata *provider.Manager
}

func newWebFixture(t *testing.T, tasks ...config.TaskInput) *webFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	text := "timezone: Asia/Shanghai\nschedule: '0 3 * * *'\nquality: lossless\napi:\n  key_env: " + webTestKeyEnv + "\nmetadata:\n  netease_cookie_env: MELOPORT_WEB_TEST_COOKIE\nserver:\n  listen: '127.0.0.1:0'\ntasks: []\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(webTestKeyEnv, webTestKey)
	t.Setenv("MELOPORT_WEB_TEST_COOKIE", webTestCookie)
	editor := config.NewEditor(path)
	for _, task := range tasks {
		if _, err := editor.Upsert("", task, true); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(cfg.Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	f := &webFixture{path: path, cfg: cfg, store: store, queue: newTaskQueue(1024), sources: map[string]*webTestSource{}, gateway: &webTestGateway{}}
	readers := map[string]provider.Source{}
	for _, source := range []string{"netease", "qq", "kugou"} {
		reader := &webTestSource{}
		f.sources[source] = reader
		readers[source] = reader
	}
	f.metadata = provider.New(readers, f.gateway, cfg.Metadata.FallbackToAPI, discardServiceLogger())
	f.control = newWebController(cfg, cfg.Tasks, f.queue, discardServiceLogger(), serviceOptions{ConfigPath: path, Store: store, Metadata: f.metadata})
	if err := f.control.install(cfg, cfg.Tasks, true); err != nil {
		store.Close()
		t.Fatal(err)
	}
	f.queue.resolve = f.control.resolve
	f.ready.Store(true)
	f.handler = f.control.handler(healthHandler(context.Background(), store, &f.ready), &f.ready)
	t.Cleanup(func() { f.control.stop(); store.Close() })
	return f
}

func webTask(id, source string) config.TaskInput {
	return config.TaskInput{ID: id, Platform: source, Playlist: "123", Enabled: true}
}

func webRequest(t *testing.T, f *webFixture, method, path string, value any, changes ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if value != nil {
		if raw, ok := value.(string); ok {
			body = []byte(raw)
		} else {
			var err error
			body, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	r := httptest.NewRequest(method, "http://localhost:8080"+path, bytes.NewReader(body))
	r.Header.Set("X-Meloport-Request", "1")
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost:8080")
	}
	for _, change := range changes {
		change(r)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), webTestKey) || strings.Contains(w.Body.String(), webTestCookie) {
		t.Fatal("private process credential leaked in Web response")
	}
	return w
}

func webStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP%d, want%d: %s", w.Code, want, w.Body.String())
	}
}

type webOverview struct {
	Timezone      string                                            `json:"timezone"`
	Schedule      string                                            `json:"schedule"`
	Quality       string                                            `json:"quality"`
	Fallback      bool                                              `json:"fallback_to_api"`
	KeyConfigured bool                                              `json:"api_key_configured"`
	KeyEnv        string                                            `json:"api_key_env"`
	Tasks         []taskView                                        `json:"tasks"`
	Runs          []state.Run                                       `json:"runs"`
	Queue         queueView                                         `json:"queue"`
	Stats         struct{ Tasks, Enabled, Downloaded, Bytes int64 } `json:"stats"`
}

func webOverviewOf(t *testing.T, f *webFixture) webOverview {
	t.Helper()
	w := webRequest(t, f, http.MethodGet, "/api/overview", nil)
	webStatus(t, w, 200)
	var value webOverview
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestWebStaticRoutesOverviewAndKeyRedaction(t *testing.T) {
	f := newWebFixture(t)
	for _, path := range []string{"/", "/assets/app.js", "/assets/app.css", "/healthz", "/readyz"} {
		w := webRequest(t, f, http.MethodGet, path, nil)
		webStatus(t, w, 200)
		policy := w.Header().Get("Content-Security-Policy")
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || policy == "" {
			t.Fatal("missing browser security headers")
		}
		if path == "/" && !strings.Contains(policy, "img-src 'self' blob:") {
			t.Fatalf("CSP does not permit locally fetched QR images: %q", policy)
		}
	}
	webStatus(t, webRequest(t, f, http.MethodGet, "/unknown", nil), 404)
	redirect := webRequest(t, f, http.MethodGet, "/assets/../config.yaml", nil)
	if redirect.Code < 300 || redirect.Code >= 400 || redirect.Header().Get("Location") != "/config.yaml" {
		t.Fatalf("path was not normalized safely: HTTP%d", redirect.Code)
	}
	webStatus(t, webRequest(t, f, http.MethodGet, redirect.Header().Get("Location"), nil), 404)
	webStatus(t, webRequest(t, f, http.MethodGet, "/api/not-found", nil), 404)
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/not-found", map[string]any{}), 404)
	webStatus(t, webRequest(t, f, http.MethodPost, "/", nil), 405)
	initial := webOverviewOf(t, f)
	if initial.Stats.Tasks != 0 || initial.Stats.Downloaded != 0 || initial.Stats.Enabled != 0 || len(initial.Tasks) != 0 || initial.Tasks == nil || initial.Runs == nil || !initial.KeyConfigured || initial.KeyEnv != webTestKeyEnv {
		t.Fatalf("bad empty overview %+v", initial)
	}
	for _, key := range []string{"", "chksz_invalid with spaces", "invalid-prefix"} {
		t.Setenv(webTestKeyEnv, key)
		if webOverviewOf(t, f).KeyConfigured {
			t.Fatal("key indicator accepted invalid/missing key")
		}
	}
	f.ready.Store(false)
	webStatus(t, webRequest(t, f, http.MethodGet, "/api/overview", nil), 503)
	webStatus(t, webRequest(t, f, http.MethodGet, "/", nil), 200)
}

func TestWebLocalKeyStatusAndRedaction(t *testing.T) {
	f := newWebFixture(t)
	t.Setenv(webTestKeyEnv, "")
	const key = "chksz_web_config_fixture"
	f.control.cfg.API.Key = key
	w := webRequest(t, f, http.MethodGet, "/api/overview", nil)
	webStatus(t, w, 200)
	if !webOverviewOf(t, f).KeyConfigured || strings.Contains(w.Body.String(), key) {
		t.Fatal("local key status is incorrect or the key was exposed")
	}
}

func TestWebTaskCRUDPersistenceScheduleInheritanceAndNoAudioDeletion(t *testing.T) {
	f := newWebFixture(t)
	one, two := webTask("one", "netease"), webTask("two", "qq")
	two.Schedule = "5 6 * * *"
	two.Quality = "320k"
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/tasks", one), 201)
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/tasks", two), 201)
	var overview webOverview
	deadline := time.Now().Add(2 * time.Second)
	for {
		overview = webOverviewOf(t, f)
		if len(overview.Tasks) == 2 && overview.Tasks[0].NextRun != "" && overview.Tasks[1].NextRun != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("next execution was not scheduled")
		}
		time.Sleep(time.Millisecond)
	}
	if overview.Tasks[0].Schedule != "" || overview.Tasks[0].EffectiveSchedule != "0 3 * * *" || overview.Tasks[0].EffectiveQuality != "lossless" {
		t.Fatalf("inheritance lost: %+v", overview.Tasks[0])
	}
	if _, err := time.Parse(time.RFC3339, overview.Tasks[0].NextRun); err != nil {
		t.Fatal("invalid next execution timestamp", err)
	}
	settings := config.SettingsInput{Timezone: "UTC", Schedule: "17 4 * * *", Quality: "master", FallbackToAPI: false}
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", settings), 200)
	overview = webOverviewOf(t, f)
	if overview.Timezone != "UTC" || overview.Schedule != "17 4 * * *" || overview.Fallback {
		t.Fatal("settings were not applied")
	}
	if overview.Tasks[0].EffectiveSchedule != "17 4 * * *" || overview.Tasks[0].EffectiveQuality != "jymaster" || overview.Tasks[0].Schedule != "" || overview.Tasks[0].Quality != "" {
		t.Fatalf("global inheritance broken: %+v", overview.Tasks[0])
	}
	if overview.Tasks[1].EffectiveSchedule != "5 6 * * *" || overview.Tasks[1].EffectiveQuality != "320k" {
		t.Fatal("global update overwrote task overrides")
	}
	one.Playlist = "456"
	one.Enabled = false
	one.Schedule = "30 5 * * *"
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/tasks/one", one), 200)
	cfg, err := config.Load(f.path)
	if err != nil || len(cfg.Tasks) != 2 || cfg.Tasks[0].PlaylistID != "456" || cfg.Tasks[0].IsEnabled() {
		t.Fatalf("persisted task wrong: %+v %v", cfg, err)
	}
	audio := filepath.Join(cfg.Tasks[0].Directory, "netease-123.flac")
	if err := os.MkdirAll(filepath.Dir(audio), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("saved music"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveDownload(context.Background(), state.Download{TaskID: "one", Provider: "netease", TrackID: "123", Path: audio, Size: 11, SHA256: "fixture"}); err != nil {
		t.Fatal(err)
	}
	webStatus(t, webRequest(t, f, http.MethodDelete, "/api/tasks/one", nil), 200)
	if data, err := os.ReadFile(audio); err != nil || string(data) != "saved music" {
		t.Fatal("deleting subscription deleted its local audio")
	}
	cfg, err = config.Load(f.path)
	if err != nil || len(cfg.Tasks) != 1 || cfg.Tasks[0].ID != "two" {
		t.Fatalf("delete not persisted: %+v %v", cfg, err)
	}
	if _, found, err := f.store.GetDownload(context.Background(), "one", "netease", "123"); err != nil || !found {
		t.Fatal("deleting task removed saved music record")
	}
	f.sources["netease"].fail.Store(true)
	webStatus(t, webRequest(t, f, http.MethodGet, "/api/search?platform=netease&keyword=test", nil), 502)
	if f.gateway.calls.Load() != 0 {
		t.Fatal("Web fallback setting was not applied to metadata manager")
	}
}

func TestWebOverviewUsesEffectivePlatformForLegacyTask(t *testing.T) {
	f := newWebFixture(t, webTask("legacy", ""))
	overview := webOverviewOf(t, f)
	if len(overview.Tasks) != 1 || overview.Tasks[0].Platform != "netease" {
		t.Fatalf("legacy task platform was not defaulted: %+v", overview.Tasks)
	}
}

func TestWebInvalidEditsKeepConfigurationBytes(t *testing.T) {
	f := newWebFixture(t, webTask("existing", "netease"))
	original, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"unknown field", "POST", "/api/tasks", `{"id":"bad","playlist":"123","enabled":true,"api_key":"must-not-be-accepted"}`, 400},
		{"missing id", "POST", "/api/tasks", `{"playlist":"123","enabled":true}`, 400},
		{"unsafe id", "POST", "/api/tasks", `{"id":"../outside","playlist":"123","enabled":true}`, 400},
		{"invalid playlist", "POST", "/api/tasks", `{"id":"bad","playlist":"https://evil.example/playlist?id=123","enabled":true}`, 400},
		{"credential playlist", "POST", "/api/tasks", `{"id":"bad","playlist":"https://music.163.com/playlist?id=123&apikey=chksz_do_not_echo_this","enabled":true}`, 400},
		{"credential fragment", "POST", "/api/tasks", `{"id":"bad","playlist":"https://music.163.com/#/playlist?id=123&token=must-not-save","enabled":true}`, 400},
		{"bad cron", "POST", "/api/tasks", `{"id":"bad","playlist":"123","schedule":"0 0 0 * * *","enabled":true}`, 400},
		{"bad quality", "POST", "/api/tasks", `{"id":"bad","playlist":"123","quality":"flac","enabled":true}`, 400},
		{"duplicate", "POST", "/api/tasks", `{"id":"existing","playlist":"456","enabled":true}`, 409},
		{"case duplicate", "POST", "/api/tasks", `{"id":"EXISTING","playlist":"456","enabled":true}`, 409},
		{"missing task", "PUT", "/api/tasks/absent", `{"id":"absent","playlist":"456","enabled":true}`, 404},
		{"rename forbidden", "PUT", "/api/tasks/existing", `{"id":"renamed","playlist":"123","enabled":true}`, 409},
		{"delete missing", "DELETE", "/api/tasks/absent", `{}`, 404},
		{"invalid JSON", "POST", "/api/tasks", `{`, 400},
		{"multiple documents", "POST", "/api/tasks", `{"id":"bad","playlist":"123"} {}`, 400},
		{"wrong type", "POST", "/api/tasks", `{"id":123,"playlist":"123"}`, 400},
		{"invalid settings", "PUT", "/api/settings", `{"timezone":"Bad/Timezone","schedule":"0 3 * * *","quality":"lossless","fallback_to_api":false}`, 400},
		{"settings unknown field", "PUT", "/api/settings", `{"timezone":"UTC","schedule":"0 3 * * *","quality":"lossless","fallback_to_api":false,"api_key":"secret"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := webRequest(t, f, tc.method, tc.path, tc.body)
			webStatus(t, w, tc.status)
			if strings.Contains(w.Body.String(), "chksz_do_not_echo_this") {
				t.Fatal("rejected credential URL echoed secret")
			}
			after, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("invalid edit changed configuration bytes")
			}
		})
	}
	oversized := `{"id":"oversized","playlist":"123","directory":"` + strings.Repeat("a", 65<<10) + `","enabled":true}`
	webStatus(t, webRequest(t, f, "POST", "/api/tasks", oversized), 400)
	after, _ := os.ReadFile(f.path)
	if !bytes.Equal(original, after) {
		t.Fatal("oversized request changed configuration")
	}
}

func TestWebOriginHostAndMetadataRequestGuards(t *testing.T) {
	f := newWebFixture(t, webTask("one", "netease"))
	original, _ := os.ReadFile(f.path)
	for name, change := range map[string]func(*http.Request){
		"foreign origin":     func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"foreign port":       func(r *http.Request) { r.Header.Set("Origin", "http://localhost:9090") },
		"HTTPS origin":       func(r *http.Request) { r.Header.Set("Origin", "https://localhost:8080") },
		"origin path":        func(r *http.Request) { r.Header.Set("Origin", "http://localhost:8080/path") },
		"missing marker":     func(r *http.Request) { r.Header.Del("X-Meloport-Request") },
		"wrong content type": func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"cross site":         func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"external Host":      func(r *http.Request) { r.Host = "evil.example"; r.Header.Set("Origin", "http://evil.example") },
	} {
		t.Run(name, func(t *testing.T) {
			webStatus(t, webRequest(t, f, "POST", "/api/tasks", webTask("new", "netease"), change), 403)
			after, _ := os.ReadFile(f.path)
			if !bytes.Equal(original, after) {
				t.Fatal("rejected browser request changed configuration")
			}
		})
	}
	webStatus(t, webRequest(t, f, "GET", "/api/overview", nil, func(r *http.Request) { r.Host = "external.example" }), 403)
	webStatus(t, webRequest(t, f, "GET", "/api/overview", nil, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }), 403)
	for _, path := range []string{"/api/search?keyword=Song", "/api/rankings?platform=netease", "/api/rankings/netease/19723756", "/api/tasks/one/playlist"} {
		webStatus(t, webRequest(t, f, "GET", path, nil, func(r *http.Request) { r.Header.Del("X-Meloport-Request") }), 403)
	}
	if f.sources["netease"].searches.Load()+f.sources["netease"].playlists.Load()+f.sources["netease"].rankings.Load()+f.sources["netease"].rankingReads.Load()+f.gateway.calls.Load() != 0 {
		t.Fatal("rejected foreign metadata request consumed a source")
	}
	for _, host := range []string{"localhost", "localhost:8080", "127.0.0.1:8080", "[::1]:8080"} {
		if !loopbackHost(host) {
			t.Errorf("rejected loopback Host %s", host)
		}
	}
	for _, host := range []string{"evil.example:8080", "localhost.evil.example:8080", "192.168.1.1:8080", ""} {
		if loopbackHost(host) {
			t.Errorf("accepted foreign Host %s", host)
		}
	}
}

func TestWebPreviewAndSearchUseNativeReadersAndValidateQueries(t *testing.T) {
	f := newWebFixture(t, webTask("one", "netease"))
	webStatus(t, webRequest(t, f, "GET", "/api/tasks/one/playlist", nil), 200)
	webStatus(t, webRequest(t, f, "GET", "/api/tasks/absent/playlist", nil), 404)
	for _, source := range []string{"netease", "qq", "kugou"} {
		w := webRequest(t, f, "GET", "/api/search?platform="+source+"&keyword=Song&limit=10&offset=2", nil)
		webStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), "Song") || f.sources[source].searches.Load() != 1 {
			t.Fatal("search did not use native reader")
		}
	}
	if f.sources["netease"].playlists.Load() != 1 || f.gateway.calls.Load() != 0 {
		t.Fatal("native preview/search consumed metered API")
	}
	for _, query := range []string{"", "?keyword=", "?keyword=Song&platform=unknown", "?keyword=Song&limit=abc", "?keyword=Song&limit=0", "?keyword=Song&limit=51", "?keyword=Song&offset=bad", "?keyword=Song&offset=-1", "?keyword=Song&offset=100001", "?keyword=" + strings.Repeat("a", 301)} {
		webStatus(t, webRequest(t, f, "GET", "/api/search"+query, nil), 400)
	}
	for _, source := range []string{"netease", "qq", "kugou"} {
		if f.sources[source].searches.Load() != 1 {
			t.Fatal("invalid query reached metadata reader")
		}
	}
}

func TestWebRankingsArePlatformScopedAndCached(t *testing.T) {
	f := newWebFixture(t)
	for _, source := range []string{"netease", "qq", "kugou"} {
		w := webRequest(t, f, http.MethodGet, "/api/rankings?platform="+source, nil)
		webStatus(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), "Native chart") || f.sources[source].rankings.Load() != 1 {
			t.Fatalf("ranking catalog did not use native reader for %s", source)
		}
		webStatus(t, webRequest(t, f, http.MethodGet, "/api/rankings?platform="+source, nil), http.StatusOK)
		if f.sources[source].rankings.Load() != 1 {
			t.Fatalf("ranking catalog was not served from the five-minute cache for %s", source)
		}
		webStatus(t, webRequest(t, f, http.MethodGet, "/api/rankings?platform="+source+"&refresh=1", nil), http.StatusOK)
		if f.sources[source].rankings.Load() != 2 {
			t.Fatalf("ranking refresh did not bypass cache for %s", source)
		}
	}
	webStatus(t, webRequest(t, f, http.MethodGet, "/api/rankings?platform=unknown", nil), http.StatusBadRequest)
	f.sources["qq"].fail.Store(true)
	webStatus(t, webRequest(t, f, http.MethodGet, "/api/rankings?platform=qq&refresh=1", nil), http.StatusBadGateway)
	if f.gateway.calls.Load() != 0 {
		t.Fatal("ranking lookup used the metered ChKSz gateway")
	}
}

func TestWebPreviewDispatchesRankingTasksNatively(t *testing.T) {
	task := config.TaskInput{ID: "top-hits", Platform: "netease", SourceType: "ranking", SourceID: "19723756", Enabled: true}
	f := newWebFixture(t, task)
	w := webRequest(t, f, http.MethodGet, "/api/tasks/top-hits/playlist", nil)
	webStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "Chart song") || f.sources["netease"].rankingReads.Load() != 1 || f.sources["netease"].playlists.Load() != 0 || f.gateway.calls.Load() != 0 {
		t.Fatal("ranking preview did not use the native ranking reader")
	}
}

func TestWebRankingDetailDispatchesNativeSource(t *testing.T) {
	f := newWebFixture(t)
	w := webRequest(t, f, http.MethodGet, "/api/rankings/netease/19723756", nil)
	webStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "Chart song") || f.sources["netease"].rankingReads.Load() != 1 {
		t.Fatal("ranking detail did not use native ranking reader")
	}
	webStatus(t, webRequest(t, f, http.MethodGet, "/api/rankings/unknown/19723756", nil), http.StatusBadRequest)
	webStatus(t, webRequest(t, f, http.MethodGet, "/api/rankings/netease/0", nil), http.StatusBadRequest)
}

func TestWebRunQueueCoalescesAndRejectsDisabledTasks(t *testing.T) {
	one, two, disabled := webTask("one", "netease"), webTask("two", "qq"), webTask("disabled", "netease")
	disabled.Enabled = false
	f := newWebFixture(t, one, two, disabled)
	webStatus(t, webRequest(t, f, "POST", "/api/tasks/one/run", map[string]any{}), 202)
	webStatus(t, webRequest(t, f, "POST", "/api/tasks/one/run", map[string]any{}), 409)
	webStatus(t, webRequest(t, f, "POST", "/api/tasks/disabled/run", map[string]any{}), 409)
	webStatus(t, webRequest(t, f, "POST", "/api/tasks/absent/run", map[string]any{}), 404)
	w := webRequest(t, f, "POST", "/api/run", map[string]any{})
	webStatus(t, w, 202)
	var count struct {
		Queued int `json:"queued"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &count); err != nil || count.Queued != 1 {
		t.Fatalf("run-all did not coalesce pending task: %s", w.Body.String())
	}
	webStatus(t, webRequest(t, f, "POST", "/api/run", map[string]any{}), 409)
	view := webOverviewOf(t, f)
	if len(view.Queue.Pending) != 2 || len(view.Queue.Running) != 0 {
		t.Fatalf("bad queue view: %+v", view.Queue)
	}
	if f.gateway.calls.Load() != 0 {
		t.Fatal("queueing tasks consumed the API before a worker ran")
	}
}

func startWebWorker(t *testing.T, f *webFixture, engine runner) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.queue.work(ctx, engine, discardServiceLogger()) }()
	t.Cleanup(func() { cancel(); waitServiceEvent(t, done) })
	return cancel, done
}

func TestWebQueuedTaskUsesLatestConfigurationAndSkipsRemovedOrDisabled(t *testing.T) {
	a, b, c := webTask("a", "netease"), webTask("b", "qq"), webTask("c", "netease")
	f := newWebFixture(t, a, b, c)
	webStatus(t, webRequest(t, f, "POST", "/api/run", map[string]any{}), 202)
	a.Enabled = false
	webStatus(t, webRequest(t, f, "PUT", "/api/tasks/a", a), 200)
	webStatus(t, webRequest(t, f, "DELETE", "/api/tasks/b", nil), 200)
	c.Playlist = "456"
	c.Quality = "hires"
	webStatus(t, webRequest(t, f, "PUT", "/api/tasks/c", c), 200)
	events := make(chan model.Task, 3)
	engine := serviceRunnerFunc(func(ctx context.Context, task model.Task) (syncer.Summary, error) {
		events <- task
		return syncer.Summary{Status: "success"}, nil
	})
	cancel, done := startWebWorker(t, f, engine)
	got := waitServiceEvent(t, events)
	if got.ID != "c" || got.PlaylistID != "456" || got.Quality != "hires" {
		t.Fatalf("worker ran stale or removed configuration: %+v", got)
	}
	cancel()
	waitServiceEvent(t, done)
	select {
	case extra := <-events:
		t.Fatalf("removed/disabled task ran: %+v", extra)
	default:
	}
	snapshot := f.queue.snapshot()
	if len(snapshot.Pending)+len(snapshot.Running) != 0 {
		t.Fatalf("completed/removed queue entries leaked: %+v", snapshot)
	}
}

func TestWebRunningTaskCannotBeEditedOrDeleted(t *testing.T) {
	f := newWebFixture(t, webTask("one", "netease"))
	webStatus(t, webRequest(t, f, "POST", "/api/tasks/one/run", map[string]any{}), 202)
	started := make(chan struct{}, 1)
	engine := serviceRunnerFunc(func(ctx context.Context, task model.Task) (syncer.Summary, error) {
		started <- struct{}{}
		<-ctx.Done()
		return syncer.Summary{Status: "canceled"}, ctx.Err()
	})
	cancel, done := startWebWorker(t, f, engine)
	waitServiceEvent(t, started)
	before, _ := os.ReadFile(f.path)
	changed := webTask("one", "netease")
	changed.Playlist = "456"
	webStatus(t, webRequest(t, f, "PUT", "/api/tasks/one", changed), 409)
	webStatus(t, webRequest(t, f, "DELETE", "/api/tasks/one", nil), 409)
	webStatus(t, webRequest(t, f, "POST", "/api/tasks/one/run", map[string]any{}), 409)
	view := webOverviewOf(t, f)
	if len(view.Queue.Running) != 1 || !view.Tasks[0].Running {
		t.Fatal("running task not reflected in overview")
	}
	after, _ := os.ReadFile(f.path)
	if !bytes.Equal(before, after) {
		t.Fatal("running task was modified")
	}
	cancel()
	waitServiceEvent(t, done)
}

func TestWebServeFromZeroTasksAcceptsRuntimeTaskAndReleasesPort(t *testing.T) {
	t.Setenv("MELOPORT_ADMIN_PASSWORD", adminTestPassword)
	f := newWebFixture(t)
	f.control.stop()
	ctx, cancel := context.WithCancel(context.Background())
	events := serviceEventWriter{listening: make(chan string, 1), ready: make(chan struct{}, 1)}
	logger := slog.New(slog.NewJSONHandler(events, nil))
	executed := make(chan model.Task, 1)
	engine := serviceRunnerFunc(func(ctx context.Context, task model.Task) (syncer.Summary, error) {
		executed <- task
		return syncer.Summary{Status: "success"}, nil
	})
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- serve(ctx, f.cfg, nil, engine, f.store, logger, serviceOptions{ConfigPath: f.path, Store: f.store, Metadata: f.metadata})
	}()
	t.Cleanup(func() {
		cancel()
		if err := waitServiceEvent(t, done); err != nil {
			t.Error(err)
		}
	})
	address := waitServiceEvent(t, events.listening)
	waitServiceEvent(t, events.ready)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, Jar: jar}
	request := func(method, path string, body any) int {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(method, "http://"+address+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Meloport-Request", "1")
		req.Header.Set("Origin", "http://"+address)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		content, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), webTestKey) {
			t.Fatal("live response leaked Key")
		}
		if response.StatusCode >= 400 {
			t.Fatalf("live Web request failed: HTTP%d %s", response.StatusCode, content)
		}
		return response.StatusCode
	}
	if request("POST", "/api/session", map[string]string{"username": "admin", "password": adminTestPassword}) != 200 {
		t.Fatal("administrator login failed")
	}
	if request("POST", "/api/tasks", webTask("runtime", "qq")) != 201 {
		t.Fatal("runtime create failed")
	}
	if request("POST", "/api/tasks/runtime/run", map[string]any{}) != 202 {
		t.Fatal("runtime run failed")
	}
	task := waitServiceEvent(t, executed)
	if task.ID != "runtime" || task.Platform != "qq" || task.Quality != "flac" {
		t.Fatalf("runtime task not loaded: %+v", task)
	}
	cancel()
	if err := waitServiceEvent(t, done); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("Web port not released: %v", err)
	}
	listener.Close()
}

func TestWebStoppedControllerCannotRestartSchedule(t *testing.T) {
	f := newWebFixture(t)
	f.control.stop()
	f.control.mu.RLock()
	previous := f.control.scheduler
	f.control.mu.RUnlock()
	candidate, err := config.NewEditor(f.path).Upsert("", webTask("after-stop", "netease"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.control.install(candidate, candidate.Tasks, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped controller accepted a new schedule: %v", err)
	}
	f.control.mu.RLock()
	unchanged := f.control.scheduler == previous && len(f.control.scheduler.Entries()) == 0 && len(f.control.tasks) == 0
	f.control.mu.RUnlock()
	if !unchanged {
		t.Fatal("post-stop install replaced the scheduler or registered tasks")
	}
	webStatus(t, webRequest(t, f, "POST", "/api/tasks", webTask("later", "qq")), 500)
	f.control.mu.RLock()
	defer f.control.mu.RUnlock()
	if len(f.control.scheduler.Entries()) != 0 || len(f.control.tasks) != 0 {
		t.Fatal("post-stop HTTP edit restarted the scheduler")
	}
}
