package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"meloport/internal/chksz"
	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/syncer"
	"meloport/internal/webui"
)

type runner interface {
	Run(context.Context, model.Task) (syncer.Summary, error)
}
type pinger interface{ Ping(context.Context) error }

// taskQueue coalesces ticks for a queued/running task and uses a single worker.
type taskQueue struct {
	mu       sync.Mutex
	pending  map[string]bool
	running  map[string]bool
	resolve  func(config.Task) (config.Task, bool)
	items    chan config.Task
	blocked  bool
	resumeAt time.Time
}

func newTaskQueue(capacity int) *taskQueue {
	return &taskQueue{pending: make(map[string]bool), running: make(map[string]bool), items: make(chan config.Task, capacity)}
}

func (q *taskQueue) enqueue(task config.Task) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending[task.ID] || q.blocked || time.Now().Before(q.resumeAt) {
		return false
	}
	select {
	case q.items <- task:
		q.pending[task.ID] = true
		return true
	default:
		return false
	}
}

// pauseBatch drops pending tasks sharing a failing key instead of hammering it.
func (q *taskQueue) pauseBatch(cause error) (int, time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var apiErr *chksz.APIError
	if !errors.As(cause, &apiErr) {
		return 0, time.Time{}, false
	}
	switch apiErr.Status {
	case 401, 403:
		q.blocked = true // Credentials cannot change in this running process.
	case 402:
		now := time.Now().In(time.FixedZone("UTC+8", 8*3600))
		q.resumeAt = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	default:
		wait := time.Minute
		if apiErr.RetryAfter > wait {
			wait = apiErr.RetryAfter
		}
		q.resumeAt = time.Now().Add(wait)
	}
	dropped := 0
	for {
		select {
		case task := <-q.items:
			delete(q.pending, task.ID)
			dropped++
		default:
			return dropped, q.resumeAt, q.blocked
		}
	}
}

func (q *taskQueue) complete(id string) {
	q.mu.Lock()
	delete(q.pending, id)
	delete(q.running, id)
	q.mu.Unlock()
}

func (q *taskQueue) work(ctx context.Context, engine runner, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-q.items:
			if ctx.Err() != nil {
				return
			}
			if q.resolve != nil {
				latest, ok := q.resolve(task)
				if !ok {
					q.complete(task.ID)
					continue
				}
				task = latest
			}
			q.mu.Lock()
			q.running[task.ID] = true
			q.mu.Unlock()
			logger.Info("sync started", "task", task.ID)
			summary, err := engine.Run(ctx, task.Model())
			q.complete(task.ID)
			if len(summary.TagWarnings) > 0 {
				logger.Warn("audio metadata incomplete", "task", task.ID, "warnings", summary.TagWarnings)
			}
			if err != nil {
				logger.Error("sync finished", "task", task.ID, "status", summary.Status, "downloaded", summary.Downloaded, "skipped", summary.Skipped, "error", err.Error())
				if chksz.IsAccountError(err) {
					dropped, resumeAt, restart := q.pauseBatch(err)
					logger.Error("shared API batch paused", "dropped_tasks", dropped, "restart_required", restart, "resume_after", resumeAt)
				}
			} else {
				logger.Info("sync finished", "task", task.ID, "status", summary.Status, "downloaded", summary.Downloaded, "skipped", summary.Skipped)
			}
		}
	}
}

func healthHandler(ctx context.Context, store pinger, ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() || ctx.Err() != nil {
			http.Error(w, "stopping", http.StatusServiceUnavailable)
			return
		}
		checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.Ping(checkCtx); err != nil {
			http.Error(w, "state unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ready")
	})
	return mux
}

func serve(parent context.Context, cfg *config.Config, tasks []config.Task, engine runner, store pinger, logger *slog.Logger, settings ...serviceOptions) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var options serviceOptions
	if len(settings) > 0 {
		options = settings[0]
	}
	if options.Audio == nil {
		options.Audio, _ = engine.(audioLibrary)
	}
	if err := webui.ValidateDirectory(options.WebDir); err != nil {
		return err
	}
	allTasks := tasks
	if options.ConfigPath != "" {
		allTasks = cfg.Tasks
	}
	queue := newTaskQueue(max(1024, len(allTasks)))
	control := newWebController(cfg, allTasks, queue, logger, options)
	queue.resolve = control.resolve
	var ready atomic.Bool
	var server *http.Server
	var healthListener net.Listener
	serverErrors := make(chan error, 1)
	if cfg.Server.Listen != "" {
		listener, err := net.Listen("tcp", cfg.Server.Listen)
		if err != nil {
			return fmt.Errorf("listen for local web management: %w", err)
		}
		healthListener = listener
		server = &http.Server{
			Handler:           control.handler(healthHandler(ctx, store, &ready), &ready),
			BaseContext:       func(net.Listener) context.Context { return ctx },
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 30 * time.Second,
		}
		go func() { serverErrors <- server.Serve(listener) }()
		logger.Info("local web server listening", "address", listener.Addr().String())
	}
	if err := control.install(cfg, allTasks, false); err != nil {
		if healthListener != nil {
			_ = healthListener.Close()
		}
		if server != nil {
			_ = server.Close()
			<-serverErrors
		}
		return fmt.Errorf("schedule tasks: %w", err)
	}
	for _, task := range allTasks {
		logger.Info("task registered", "task", task.ID, "schedule", task.Schedule, "timezone", cfg.Timezone)
	}
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); queue.work(ctx, engine, logger) }()
	control.mu.Lock()
	control.scheduler.Start()
	control.mu.Unlock()
	if cfg.RunOnStart {
		for _, task := range allTasks {
			if task.IsEnabled() {
				queue.enqueue(task)
			}
		}
	}
	ready.Store(true)
	logger.Info("service ready", "tasks", len(tasks), "run_on_start", cfg.RunOnStart)
	var result error
	select {
	case <-parent.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("local web server: %w", err)
		}
	}
	ready.Store(false)
	cancel()
	control.stop()
	if server != nil {
		shutdownCtx, stop := cleanupContext()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		stop()
	}
	<-workerDone
	logger.Info("service stopped")
	return result
}
