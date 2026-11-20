package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meloport/internal/chksz"
	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/syncer"
)

type serviceRunnerFunc func(context.Context, model.Task) (syncer.Summary, error)

func (f serviceRunnerFunc) Run(ctx context.Context, task model.Task) (syncer.Summary, error) {
	return f(ctx, task)
}

type servicePingerFunc func(context.Context) error

func (f servicePingerFunc) Ping(ctx context.Context) error { return f(ctx) }

func discardServiceLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func waitServiceEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for service event")
		var zero T
		return zero
	}
}

func TestTaskQueueCoalescesQueuedTasksAndRejectsFullQueue(t *testing.T) {
	queue := newTaskQueue(1)
	one := config.Task{ID: "one"}
	two := config.Task{ID: "two"}
	if !queue.enqueue(one) {
		t.Fatal("first task was not queued")
	}
	if queue.enqueue(one) {
		t.Fatal("duplicate queued task was accepted")
	}
	if queue.enqueue(two) {
		t.Fatal("full queue accepted another task")
	}
	if got := <-queue.items; got.ID != one.ID {
		t.Fatalf("unexpected queued task: %s", got.ID)
	}
	queue.complete(one.ID)
	if !queue.enqueue(two) {
		t.Fatal("a task rejected because of capacity was incorrectly marked pending")
	}
}

func TestTaskQueueSerializesRunsAndCoalescesRunningTasks(t *testing.T) {
	queue := newTaskQueue(2)
	one := config.Task{ID: "one", PlaylistID: "123", Quality: "lossless", Directory: "one-directory"}
	two := config.Task{ID: "two", PlaylistID: "456", Quality: "hires", Directory: "two-directory"}
	started := make(chan model.Task, 3)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	engine := serviceRunnerFunc(func(ctx context.Context, task model.Task) (syncer.Summary, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximum.Load()
			if count <= previous || maximum.CompareAndSwap(previous, count) {
				break
			}
		}
		started <- task
		select {
		case <-release:
			return syncer.Summary{Status: "success"}, nil
		case <-ctx.Done():
			return syncer.Summary{Status: "interrupted"}, ctx.Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	if !queue.enqueue(one) || queue.enqueue(one) {
		t.Fatal("queued duplicate coalescing failed")
	}
	go func() {
		defer close(done)
		queue.work(ctx, engine, discardServiceLogger())
	}()
	t.Cleanup(func() {
		cancel()
		waitServiceEvent(t, done)
	})
	if got := waitServiceEvent(t, started); got != one.Model() {
		t.Fatalf("task model was changed: %+v", got)
	}
	if queue.enqueue(one) {
		t.Fatal("running task was queued again")
	}
	if !queue.enqueue(two) || queue.enqueue(two) {
		t.Fatal("second task was not coalesced while first ran")
	}
	select {
	case unexpected := <-started:
		t.Fatalf("another task started while first was blocked: %s", unexpected.ID)
	default:
	}
	release <- struct{}{}
	if got := waitServiceEvent(t, started); got.ID != two.ID {
		t.Fatalf("wrong second task: %+v", got)
	}
	if !queue.enqueue(one) || queue.enqueue(one) {
		t.Fatal("completed task could not be queued once")
	}
	release <- struct{}{}
	if got := waitServiceEvent(t, started); got.ID != one.ID {
		t.Fatalf("wrong repeated task: %+v", got)
	}
	cancel()
	waitServiceEvent(t, done)
	if maximum.Load() != 1 || active.Load() != 0 {
		t.Fatalf("worker was concurrent or remained active: maximum=%d active=%d", maximum.Load(), active.Load())
	}
}

func TestTaskQueueCancellationDoesNotStartPendingWork(t *testing.T) {
	queue := newTaskQueue(1)
	queue.enqueue(config.Task{ID: "queued"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	engine := serviceRunnerFunc(func(context.Context, model.Task) (syncer.Summary, error) {
		calls.Add(1)
		return syncer.Summary{}, nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		queue.work(ctx, engine, discardServiceLogger())
	}()
	waitServiceEvent(t, done)
	if calls.Load() != 0 {
		t.Fatal("worker started pending work after cancellation")
	}
}

func TestTaskQueuePauseBatchDropsQueuedTasksAndSuppressesEnqueue(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter time.Duration
		blocked    bool
		wait       time.Duration
		quota      bool
	}{
		{name: "invalid key", status: http.StatusUnauthorized, blocked: true},
		{name: "forbidden key", status: http.StatusForbidden, blocked: true},
		{name: "quota exhausted", status: http.StatusPaymentRequired, quota: true},
		{name: "rate limit minimum", status: http.StatusTooManyRequests, wait: time.Minute},
		{name: "rate limit retry after", status: http.StatusTooManyRequests, retryAfter: 2 * time.Minute, wait: 2 * time.Minute},
		{name: "service unavailable", status: http.StatusServiceUnavailable, retryAfter: 2 * time.Second, wait: time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := newTaskQueue(2)
			queue.enqueue(config.Task{ID: "one"})
			queue.enqueue(config.Task{ID: "two"})
			before := time.Now()
			dropped, resumeAt, blocked := queue.pauseBatch(errors.Join(errors.New("wrapped error"), &chksz.APIError{Status: tt.status, Message: "test response", RetryAfter: tt.retryAfter}))
			after := time.Now()
			if dropped != 2 || len(queue.items) != 0 || len(queue.pending) != 0 || blocked != tt.blocked {
				t.Fatalf("invalid pause state: dropped=%d queued=%d pending=%d blocked=%t", dropped, len(queue.items), len(queue.pending), blocked)
			}
			if queue.enqueue(config.Task{ID: "one"}) || queue.enqueue(config.Task{ID: "new"}) {
				t.Fatal("paused queue accepted another task")
			}
			switch {
			case tt.blocked:
				if !resumeAt.IsZero() {
					t.Fatal("authentication failure should require restart, not an automatic retry")
				}
			case tt.quota:
				_, offset := resumeAt.Zone()
				if !resumeAt.After(before) || resumeAt.Sub(before) > 24*time.Hour || resumeAt.Hour() != 0 || resumeAt.Minute() != 0 || resumeAt.Second() != 0 || offset != 8*3600 {
					t.Fatalf("quota did not pause until the next UTC+8 midnight: %s", resumeAt)
				}
			default:
				if resumeAt.Before(before.Add(tt.wait)) || resumeAt.After(after.Add(tt.wait)) {
					t.Fatalf("cooldown does not honor minimum/retry-after: %s (wait %s)", resumeAt, tt.wait)
				}
			}
			if !tt.blocked {
				queue.mu.Lock()
				queue.resumeAt = time.Now().Add(-time.Second)
				queue.mu.Unlock()
				if !queue.enqueue(config.Task{ID: "one"}) {
					t.Fatal("expired pause did not allow future scheduled work")
				}
			}
		})
	}
}

func TestTaskQueuePauseBatchIgnoresNonAPIErrors(t *testing.T) {
	queue := newTaskQueue(2)
	queue.enqueue(config.Task{ID: "one"})
	dropped, resumeAt, blocked := queue.pauseBatch(errors.New("a single song failed"))
	if dropped != 0 || !resumeAt.IsZero() || blocked || len(queue.items) != 1 || !queue.enqueue(config.Task{ID: "two"}) {
		t.Fatalf("ordinary error paused unrelated tasks: dropped=%d resume=%s blocked=%t", dropped, resumeAt, blocked)
	}
}

func TestTaskQueueWorkerPausesSharedAccountBatch(t *testing.T) {
	queue := newTaskQueue(2)
	queue.enqueue(config.Task{ID: "first"})
	queue.enqueue(config.Task{ID: "second"})
	var calls atomic.Int32
	engine := serviceRunnerFunc(func(context.Context, model.Task) (syncer.Summary, error) {
		calls.Add(1)
		return syncer.Summary{Status: "failed"}, &chksz.APIError{Status: http.StatusUnauthorized, Message: "invalid key"}
	})
	events := serviceEventWriter{paused: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		queue.work(ctx, engine, slog.New(slog.NewJSONHandler(events, nil)))
	}()
	t.Cleanup(func() {
		cancel()
		waitServiceEvent(t, done)
	})
	waitServiceEvent(t, events.paused)
	if calls.Load() != 1 || len(queue.items) != 0 || queue.enqueue(config.Task{ID: "third"}) {
		t.Fatalf("worker kept querying a failing shared account: calls=%d queued=%d", calls.Load(), len(queue.items))
	}
	cancel()
	waitServiceEvent(t, done)
}

func TestTaskQueueWorkerContinuesAfterOrdinaryTaskFailure(t *testing.T) {
	queue := newTaskQueue(2)
	queue.enqueue(config.Task{ID: "first"})
	queue.enqueue(config.Task{ID: "second"})
	started := make(chan string, 2)
	engine := serviceRunnerFunc(func(_ context.Context, task model.Task) (syncer.Summary, error) {
		started <- task.ID
		if task.ID == "first" {
			return syncer.Summary{Status: "failed"}, &chksz.APIError{Status: http.StatusBadRequest, Message: "invalid playlist"}
		}
		return syncer.Summary{Status: "success"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		queue.work(ctx, engine, discardServiceLogger())
	}()
	t.Cleanup(func() {
		cancel()
		waitServiceEvent(t, done)
	})
	if waitServiceEvent(t, started) != "first" || waitServiceEvent(t, started) != "second" {
		t.Fatal("ordinary task failure prevented subsequent subscription work")
	}
	cancel()
	waitServiceEvent(t, done)
}

func TestHealthHandlerReadiness(t *testing.T) {
	tests := []struct {
		name      string
		ready     bool
		canceled  bool
		pingError error
		status    int
		body      string
		pingCalls int32
	}{
		{name: "ready", ready: true, status: http.StatusOK, body: "ready\n", pingCalls: 1},
		{name: "not ready", status: http.StatusServiceUnavailable, body: "stopping\n"},
		{name: "stopping", ready: true, canceled: true, status: http.StatusServiceUnavailable, body: "stopping\n"},
		{name: "state error", ready: true, pingError: errors.New("private database diagnostic"), status: http.StatusServiceUnavailable, body: "state unavailable\n", pingCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			var ready atomic.Bool
			ready.Store(tt.ready)
			var calls atomic.Int32
			store := servicePingerFunc(func(checkCtx context.Context) error {
				calls.Add(1)
				deadline, hasDeadline := checkCtx.Deadline()
				if !hasDeadline || time.Until(deadline) > 2*time.Second || checkCtx.Err() != nil {
					t.Error("database readiness check has no valid bounded context")
				}
				return tt.pingError
			})
			handler := healthHandler(ctx, store, &ready)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if recorder.Code != tt.status || recorder.Body.String() != tt.body || calls.Load() != tt.pingCalls {
				t.Fatalf("ready response: status=%d body=%q pings=%d", recorder.Code, recorder.Body.String(), calls.Load())
			}
			if strings.Contains(recorder.Body.String(), "private") {
				t.Fatal("readiness response leaked database diagnostics")
			}
			liveness := httptest.NewRecorder()
			handler.ServeHTTP(liveness, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if liveness.Code != http.StatusOK || liveness.Body.String() != "ok\n" || calls.Load() != tt.pingCalls {
				t.Fatalf("unexpected liveness response: status=%d body=%q pings=%d", liveness.Code, liveness.Body.String(), calls.Load())
			}
		})
	}
}

func TestHealthHandlerAllowsOnlyKnownGetEndpoints(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	handler := healthHandler(context.Background(), servicePingerFunc(func(context.Context) error { return nil }), &ready)
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodPost, "/readyz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/unknown", http.StatusNotFound},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if recorder.Code != tc.status {
			t.Fatalf("%s %s status=%d want=%d", tc.method, tc.path, recorder.Code, tc.status)
		}
	}
}

// serviceEventWriter captures only synchronization events, avoiding concurrent
// reads of a bytes.Buffer while the worker and HTTP server are logging.
type serviceEventWriter struct {
	listening chan string
	ready     chan struct{}
	paused    chan struct{}
}

func (w serviceEventWriter) Write(data []byte) (int, error) {
	var event struct {
		Message string `json:"msg"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return len(data), nil
	}
	switch event.Message {
	case "local web server listening":
		select {
		case w.listening <- event.Address:
		default:
		}
	case "service ready":
		select {
		case w.ready <- struct{}{}:
		default:
		}
	case "shared API batch paused":
		select {
		case w.paused <- struct{}{}:
		default:
		}
	}
	return len(data), nil
}

func TestServeRunOnStartAndCancellationReleaseListener(t *testing.T) {
	cfg := &config.Config{
		Timezone:   "UTC",
		Location:   time.UTC,
		RunOnStart: true,
		Server:     config.Server{Listen: "127.0.0.1:0"},
	}
	task := config.Task{ID: "subscription", PlaylistID: "123", Quality: "lossless", Directory: t.TempDir(), Schedule: "0 0 1 1 *"}
	started := make(chan model.Task, 1)
	stopped := make(chan struct{}, 1)
	engine := serviceRunnerFunc(func(ctx context.Context, got model.Task) (syncer.Summary, error) {
		started <- got
		<-ctx.Done()
		stopped <- struct{}{}
		return syncer.Summary{Status: "interrupted"}, ctx.Err()
	})
	events := serviceEventWriter{listening: make(chan string, 1), ready: make(chan struct{}, 1)}
	logger := slog.New(slog.NewJSONHandler(events, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = serve(ctx, cfg, []config.Task{task}, engine, servicePingerFunc(func(context.Context) error { return nil }), logger)
	}()
	t.Cleanup(func() {
		cancel()
		waitServiceEvent(t, done)
	})
	address := waitServiceEvent(t, events.listening)
	waitServiceEvent(t, events.ready)
	if got := waitServiceEvent(t, started); got != task.Model() {
		t.Fatalf("run_on_start ran wrong task: %+v", got)
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + address + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	client.CloseIdleConnections()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("live service never became ready: HTTP %d", response.StatusCode)
	}
	cancel()
	waitServiceEvent(t, done)
	waitServiceEvent(t, stopped)
	if serveErr != nil {
		t.Fatalf("normal shutdown returned an error: %v", serveErr)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("service shutdown retained its listening port: %v", err)
	}
	_ = listener.Close()
}

func TestServeInvalidScheduleClosesListener(t *testing.T) {
	cfg := &config.Config{Timezone: "UTC", Location: time.UTC, Server: config.Server{Listen: "127.0.0.1:0"}}
	events := serviceEventWriter{listening: make(chan string, 1), ready: make(chan struct{}, 1)}
	var runs atomic.Int32
	engine := serviceRunnerFunc(func(context.Context, model.Task) (syncer.Summary, error) {
		runs.Add(1)
		return syncer.Summary{}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := serve(ctx, cfg, []config.Task{{ID: "invalid", Schedule: "invalid cron"}}, engine, servicePingerFunc(func(context.Context) error { return nil }), slog.New(slog.NewJSONHandler(events, nil)))
	if err == nil || runs.Load() != 0 {
		t.Fatalf("invalid schedule accepted or worker started: error=%v runs=%d", err, runs.Load())
	}
	address := waitServiceEvent(t, events.listening)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("invalid schedule leaked a listener: %v", err)
	}
	_ = listener.Close()
}
