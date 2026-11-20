package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"meloport/internal/chksz"
	"meloport/internal/config"
	"meloport/internal/provider"
	"meloport/internal/state"
	"meloport/internal/webui"
)

type serviceOptions struct {
	ConfigPath string
	WebDir     string
	Store      *state.Store
	Metadata   *provider.Manager
}

type webController struct {
	mu        sync.RWMutex
	changes   sync.Mutex
	cfg       *config.Config
	tasks     map[string]config.Task
	entries   map[string]cron.EntryID
	scheduler *cron.Cron
	queue     *taskQueue
	store     *state.Store
	metadata  *provider.Manager
	editor    *config.Editor
	logger    *slog.Logger
	webDir    string
	stopping  bool
}

func newWebController(cfg *config.Config, tasks []config.Task, queue *taskQueue, logger *slog.Logger, options serviceOptions) *webController {
	c := &webController{cfg: cfg, queue: queue, logger: logger, store: options.Store, metadata: options.Metadata, webDir: options.WebDir, tasks: make(map[string]config.Task), entries: make(map[string]cron.EntryID), scheduler: cron.New(cron.WithLocation(cfg.Location))}
	if options.ConfigPath != "" {
		c.editor = config.NewEditor(options.ConfigPath)
	}
	for _, task := range tasks {
		c.tasks[task.ID] = task
	}
	return c
}

// install replaces the schedule only after the entire candidate is valid.
func (c *webController) install(cfg *config.Config, tasks []config.Task, start bool) error {
	next := cron.New(cron.WithLocation(cfg.Location))
	entries := make(map[string]cron.EntryID)
	current := make(map[string]config.Task)
	for _, task := range tasks {
		current[task.ID] = task
		if !task.IsEnabled() {
			continue
		}
		task := task
		id, err := next.AddFunc(task.Schedule, func() {
			if !c.queue.enqueue(task) {
				c.logger.Info("sync tick skipped: already pending or API paused", "task", task.ID)
			}
		})
		if err != nil {
			return err
		}
		entries[task.ID] = id
	}
	c.mu.Lock()
	if c.stopping {
		c.mu.Unlock()
		return context.Canceled
	}
	previous := c.scheduler
	c.cfg, c.tasks, c.scheduler, c.entries = cfg, current, next, entries
	if previous != nil {
		<-previous.Stop().Done()
	}
	if c.metadata != nil {
		c.metadata.SetFallback(cfg.Metadata.FallbackToAPI)
	}
	if start {
		next.Start()
	}
	c.mu.Unlock()
	return nil
}

func (c *webController) stop() {
	c.changes.Lock()
	defer c.changes.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopping = true
	<-c.scheduler.Stop().Done()
}

func (c *webController) resolve(task config.Task) (config.Task, bool) {
	c.changes.Lock()
	defer c.changes.Unlock()
	c.mu.RLock()
	defer c.mu.RUnlock()
	latest, found := c.tasks[task.ID]
	if !found || !latest.IsEnabled() {
		return config.Task{}, false
	}
	c.queue.mu.Lock()
	c.queue.running[latest.ID] = true
	c.queue.mu.Unlock()
	return latest, true
}

type queueView struct {
	Pending         []string `json:"pending"`
	Running         []string `json:"running"`
	Paused          bool     `json:"paused"`
	ResumeAt        string   `json:"resume_at"`
	RestartRequired bool     `json:"restart_required"`
}

func (q *taskQueue) snapshot() queueView {
	q.mu.Lock()
	defer q.mu.Unlock()
	view := queueView{Pending: []string{}, Running: []string{}, Paused: q.blocked || time.Now().Before(q.resumeAt), RestartRequired: q.blocked}
	if !q.resumeAt.IsZero() {
		view.ResumeAt = q.resumeAt.Format(time.RFC3339)
	}
	for id := range q.pending {
		if q.running[id] {
			view.Running = append(view.Running, id)
		} else {
			view.Pending = append(view.Pending, id)
		}
	}
	sort.Strings(view.Pending)
	sort.Strings(view.Running)
	return view
}

func (q *taskQueue) isRunning(id string) bool { q.mu.Lock(); defer q.mu.Unlock(); return q.running[id] }

func (c *webController) handler(health http.Handler, ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", health)
	mux.Handle("GET /readyz", health)
	mux.HandleFunc("GET /api/overview", c.overview)
	mux.HandleFunc("POST /api/tasks", c.createTask)
	mux.HandleFunc("PUT /api/tasks/{id}", c.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", c.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/run", c.runTask)
	mux.HandleFunc("GET /api/tasks/{id}/playlist", c.preview)
	mux.HandleFunc("GET /api/search", c.search)
	mux.HandleFunc("PUT /api/settings", c.settings)
	mux.HandleFunc("POST /api/run", c.runAll)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		webError(w, http.StatusNotFound, "unknown management endpoint")
	})
	mux.Handle("/", webui.HandlerFromDirectory(c.webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if !loopbackHost(r.Host) {
				webError(w, 403, "management requests require a loopback Host")
				return
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				webError(w, 403, "cross-site management requests are refused")
				return
			}
			if r.URL.Path == "/api/search" || strings.HasSuffix(r.URL.Path, "/playlist") {
				if r.Header.Get("X-Meloport-Request") != "1" {
					webError(w, 403, "metadata requests require the Meloport request header")
					return
				}
			}
			if ready != nil && !ready.Load() {
				webError(w, 503, "service is starting or stopping")
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				origin := r.Header.Get("Origin")
				if origin != "" {
					u, err := url.Parse(origin)
					if err != nil || u.Scheme != "http" || u.Host != r.Host || u.User != nil || u.Path != "" {
						webError(w, 403, "cross-origin management requests are refused")
						return
					}
				}
				media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if r.Header.Get("X-Meloport-Request") != "1" || media != "application/json" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
					webError(w, 403, "management writes require same-origin JSON requests")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func loopbackHost(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	return name == "localhost" || net.ParseIP(strings.Trim(name, "[]")).IsLoopback()
}

type taskView struct {
	config.TaskInput
	EffectiveSchedule string     `json:"effective_schedule"`
	EffectiveQuality  string     `json:"effective_quality"`
	NextRun           string     `json:"next_run"`
	LastRun           *state.Run `json:"last_run"`
	Queued            bool       `json:"queued"`
	Running           bool       `json:"running"`
}

func (c *webController) overview(w http.ResponseWriter, r *http.Request) {
	if c.store == nil || c.editor == nil {
		webError(w, 503, "management store is unavailable")
		return
	}
	c.changes.Lock()
	defer c.changes.Unlock()
	raw, err := c.editor.Tasks()
	if err != nil {
		webError(w, 500, "cannot read subscription configuration")
		return
	}
	runs, err := c.store.LatestRuns(r.Context(), 50)
	if err != nil {
		webError(w, 500, "cannot read synchronization history")
		return
	}
	stats, err := c.store.Statistics(r.Context())
	if err != nil {
		webError(w, 500, "cannot read file statistics")
		return
	}
	last := map[string]*state.Run{}
	for i := range runs {
		if last[runs[i].TaskID] == nil {
			last[runs[i].TaskID] = &runs[i]
		}
	}
	queue := c.queue.snapshot()
	pending, running := map[string]bool{}, map[string]bool{}
	for _, id := range queue.Pending {
		pending[id] = true
	}
	for _, id := range queue.Running {
		running[id] = true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	views := make([]taskView, 0, len(raw))
	enabled := 0
	for _, task := range raw {
		effective, ok := c.tasks[task.ID]
		if !ok {
			continue
		}
		task.Platform = effective.Platform
		view := taskView{TaskInput: task, EffectiveSchedule: effective.Schedule, EffectiveQuality: effective.Quality, LastRun: last[task.ID], Queued: pending[task.ID], Running: running[task.ID]}
		if effective.IsEnabled() {
			enabled++
			if id, ok := c.entries[task.ID]; ok {
				next := c.scheduler.Entry(id).Next
				if !next.IsZero() {
					view.NextRun = next.In(c.cfg.Location).Format(time.RFC3339)
				}
			}
		}
		views = append(views, view)
	}
	webJSON(w, 200, map[string]any{"timezone": c.cfg.Timezone, "schedule": c.cfg.Schedule, "quality": c.cfg.Quality, "fallback_to_api": c.cfg.Metadata.FallbackToAPI, "api_key_env": c.cfg.API.KeyEnv, "api_key_configured": chksz.KeyConfigured(c.cfg.API.KeyEnv), "stats": map[string]any{"tasks": len(views), "enabled": enabled, "downloaded": stats.Downloaded, "bytes": stats.Bytes}, "queue": queue, "tasks": views, "runs": runs})
}

func (c *webController) available(w http.ResponseWriter) bool {
	if c.editor == nil {
		webError(w, 503, "configuration editing is unavailable")
		return false
	}
	return true
}

func (c *webController) createTask(w http.ResponseWriter, r *http.Request) {
	if !c.available(w) {
		return
	}
	var input config.TaskInput
	if !readWebJSON(w, r, &input) {
		return
	}
	c.changes.Lock()
	defer c.changes.Unlock()
	cfg, err := c.editor.Upsert("", input, true)
	if err != nil {
		editError(w, err)
		return
	}
	if err := c.install(cfg, cfg.Tasks, true); err != nil {
		webError(w, 500, "configuration saved but schedule could not be applied; restart required")
		return
	}
	webJSON(w, 201, map[string]bool{"saved": true})
}

func (c *webController) updateTask(w http.ResponseWriter, r *http.Request) {
	if !c.available(w) {
		return
	}
	var input config.TaskInput
	if !readWebJSON(w, r, &input) {
		return
	}
	c.changes.Lock()
	defer c.changes.Unlock()
	if c.queue.isRunning(r.PathValue("id")) {
		webError(w, 409, "task is running; wait for it to finish before editing")
		return
	}
	cfg, err := c.editor.Upsert(r.PathValue("id"), input, false)
	if err != nil {
		editError(w, err)
		return
	}
	if err := c.install(cfg, cfg.Tasks, true); err != nil {
		webError(w, 500, "configuration saved but schedule could not be applied; restart required")
		return
	}
	webJSON(w, 200, map[string]bool{"saved": true})
}

func (c *webController) deleteTask(w http.ResponseWriter, r *http.Request) {
	if !c.available(w) {
		return
	}
	c.changes.Lock()
	defer c.changes.Unlock()
	if c.queue.isRunning(r.PathValue("id")) {
		webError(w, 409, "task is running; wait for it to finish before removing")
		return
	}
	cfg, err := c.editor.Delete(r.PathValue("id"))
	if err != nil {
		editError(w, err)
		return
	}
	if err := c.install(cfg, cfg.Tasks, true); err != nil {
		webError(w, 500, "configuration saved but schedule could not be applied; restart required")
		return
	}
	webJSON(w, 200, map[string]bool{"removed": true})
}

func (c *webController) settings(w http.ResponseWriter, r *http.Request) {
	if !c.available(w) {
		return
	}
	var input config.SettingsInput
	if !readWebJSON(w, r, &input) {
		return
	}
	c.changes.Lock()
	defer c.changes.Unlock()
	cfg, err := c.editor.Settings(input)
	if err != nil {
		editError(w, err)
		return
	}
	if err := c.install(cfg, cfg.Tasks, true); err != nil {
		webError(w, 500, "configuration saved but schedule could not be applied; restart required")
		return
	}
	webJSON(w, 200, map[string]bool{"saved": true})
}

func (c *webController) runTask(w http.ResponseWriter, r *http.Request) {
	c.changes.Lock()
	defer c.changes.Unlock()
	c.mu.RLock()
	task, found := c.tasks[r.PathValue("id")]
	c.mu.RUnlock()
	if !found {
		webError(w, 404, "subscription task not found")
		return
	}
	if !task.IsEnabled() {
		webError(w, 409, "enable the task before running it")
		return
	}
	if !c.queue.enqueue(task) {
		webError(w, 409, "task is pending/running, or the shared audio API is paused")
		return
	}
	webJSON(w, 202, map[string]bool{"queued": true})
}

func (c *webController) runAll(w http.ResponseWriter, r *http.Request) {
	c.changes.Lock()
	defer c.changes.Unlock()
	c.mu.RLock()
	tasks := make([]config.Task, 0, len(c.tasks))
	for _, task := range c.tasks {
		if task.IsEnabled() {
			tasks = append(tasks, task)
		}
	}
	c.mu.RUnlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	queued := 0
	for _, task := range tasks {
		if c.queue.enqueue(task) {
			queued++
		}
	}
	if queued == 0 {
		webError(w, 409, "no enabled task could be queued; tasks may already be pending or the API is paused")
		return
	}
	webJSON(w, 202, map[string]int{"queued": queued})
}

func (c *webController) preview(w http.ResponseWriter, r *http.Request) {
	if c.metadata == nil {
		webError(w, 503, "metadata readers are unavailable")
		return
	}
	c.mu.RLock()
	task, found := c.tasks[r.PathValue("id")]
	c.mu.RUnlock()
	if !found {
		webError(w, 404, "subscription task not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	playlist, err := c.metadata.Playlist(ctx, task.Platform, task.PlaylistID)
	if err != nil {
		webError(w, 502, err.Error())
		return
	}
	webJSON(w, 200, playlist)
}

func (c *webController) search(w http.ResponseWriter, r *http.Request) {
	if c.metadata == nil {
		webError(w, 503, "metadata readers are unavailable")
		return
	}
	source, keyword := r.URL.Query().Get("platform"), strings.TrimSpace(r.URL.Query().Get("keyword"))
	if source == "" {
		source = "netease"
	}
	limit := 20
	offset := 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			webError(w, 400, "invalid search limit")
			return
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			webError(w, 400, "invalid search offset")
			return
		}
	}
	if keyword == "" || len([]rune(keyword)) > 300 || limit < 1 || limit > 50 || offset < 0 || offset > 100000 {
		webError(w, 400, "provide a keyword, limit 1-50 and offset 0-100000")
		return
	}
	if source != "netease" && source != "qq" && source != "kugou" {
		webError(w, 400, "platform must be netease, qq or kugou")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	tracks, err := c.metadata.Search(ctx, source, keyword, limit, offset)
	if err != nil {
		webError(w, 502, err.Error())
		return
	}
	webJSON(w, 200, map[string]any{"platform": source, "tracks": tracks})
}

func readWebJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		webError(w, 400, "invalid JSON request; check fields and value types")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		webError(w, 400, "provide one JSON object")
		return false
	}
	return true
}

func editError(w http.ResponseWriter, err error) {
	status := 400
	if errors.Is(err, config.ErrNotFound) {
		status = 404
	}
	if errors.Is(err, config.ErrConflict) {
		status = 409
	}
	webError(w, status, err.Error())
}

func webError(w http.ResponseWriter, status int, message string) {
	webJSON(w, status, map[string]string{"error": message})
}
func webJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
