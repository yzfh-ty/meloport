package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"meloport/internal/chksz"
	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

type fakeProvider struct {
	playlist    model.Playlist
	playlistErr error
	resolveErr  map[string]error
	resolved    []string
	mu          sync.Mutex
	entered     chan struct{}
	block       chan struct{}
}

func (p *fakeProvider) Playlist(ctx context.Context, _ string) (model.Playlist, error) {
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return model.Playlist{}, ctx.Err()
		}
	}
	return p.playlist, p.playlistErr
}

func (p *fakeProvider) Resolve(_ context.Context, id, _ string) (model.ResolvedTrack, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolved = append(p.resolved, id)
	return model.ResolvedTrack{URL: "https://example.invalid/" + id, Format: "flac"}, p.resolveErr[id]
}

type fakeDownloader struct {
	calls int
}

func (d *fakeDownloader) Download(ctx context.Context, _ model.ResolvedTrack, directory, basename string) (model.DownloadResult, error) {
	if err := ctx.Err(); err != nil {
		return model.DownloadResult{}, err
	}
	d.calls++
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return model.DownloadResult{}, err
	}
	path := filepath.Join(directory, basename+".flac")
	data := []byte("fLaCcomplete audio fixture")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return model.DownloadResult{}, err
	}
	return model.DownloadResult{Path: path, Size: int64(len(data)), SHA256: "fixture-hash"}, nil
}

func setup(t *testing.T, p *fakeProvider) (*syncer.Engine, *state.Store, *fakeDownloader, model.Task) {
	t.Helper()
	directory := t.TempDir()
	store, err := state.Open(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	d := &fakeDownloader{}
	task := model.Task{ID: "favorites", PlaylistID: "123", Quality: "lossless", Directory: filepath.Join(directory, "music")}
	return syncer.New(p, d, store), store, d, task
}

func playlist(ids ...string) model.Playlist {
	result := model.Playlist{ID: "123", Name: "收藏", ReportedCount: len(ids)}
	for _, id := range ids {
		result.Tracks = append(result.Tracks, model.Track{ID: id, Name: "Song " + id, Artists: []string{"Artist"}})
	}
	return result
}

func TestIncrementalSyncDeduplicatesAndNeverDeletes(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1", "2", "2")}
	engine, store, downloader, task := setup(t, p)
	first, err := engine.Run(ctx, task)
	if err != nil || first.Downloaded != 2 || first.Total != 2 || first.Status != "success" || first.Pending != 0 {
		t.Fatalf("first sync %+v err=%v", first, err)
	}
	second, err := engine.Run(ctx, task)
	if err != nil || second.Skipped != 2 || second.Downloaded != 0 || downloader.calls != 2 || len(p.resolved) != 2 {
		t.Fatalf("second sync %+v calls=%d resolves=%v err=%v", second, downloader.calls, p.resolved, err)
	}
	p.playlist = playlist("1")
	third, err := engine.Run(ctx, task)
	if err != nil || third.Skipped != 1 {
		t.Fatalf("third sync %+v err=%v", third, err)
	}
	if _, err := os.Stat(filepath.Join(task.Directory, "netease-2.flac")); err != nil {
		t.Fatalf("removed remote track was deleted locally: %v", err)
	}
	runs, err := store.LatestRuns(ctx, 10)
	if err != nil || len(runs) != 3 || runs[0].Status != "success" {
		t.Fatalf("history %+v err=%v", runs, err)
	}
	if record, found, err := store.GetDownload(ctx, task.ID, "netease", "1"); err != nil || !found || record.Title != "Song 1" {
		t.Fatalf("metadata record %+v found=%v err=%v", record, found, err)
	}
}

func TestMissingAndTruncatedFilesAreRepairedWithBackup(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1", "2")}
	engine, _, downloader, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(task.Directory, "netease-1.flac")); err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(task.Directory, "netease-2.flac")
	if err := os.WriteFile(truncated, []byte("fLaC"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(ctx, task)
	if err != nil || result.Downloaded != 2 || result.Skipped != 0 || downloader.calls != 4 {
		t.Fatalf("repair %+v calls=%d err=%v", result, downloader.calls, err)
	}
	backups, err := filepath.Glob(truncated + ".corrupt-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	data, err := os.ReadFile(backups[0])
	if err != nil || string(data) != "fLaC" {
		t.Fatalf("backup data=%q err=%v", data, err)
	}
}

func TestChangedOutputLeavesOldFileAndDownloadsToNewDirectory(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1")}
	engine, _, _, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(task.Directory, "netease-1.flac")
	task.Directory = filepath.Join(filepath.Dir(task.Directory), "new-music")
	result, err := engine.Run(ctx, task)
	if err != nil || result.Downloaded != 1 {
		t.Fatalf("new directory %+v err=%v", result, err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old file changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(task.Directory, "netease-1.flac")); err != nil {
		t.Fatalf("new file missing: %v", err)
	}
}

func TestUnresolvableTrackDoesNotMoveExistingIncompleteFile(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1")}
	engine, _, _, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.Directory, "netease-1.flac")
	if err := os.WriteFile(path, []byte("fLaC"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.resolveErr = map[string]error{"1": &chksz.APIError{Status: 402, Message: "quota exhausted"}}
	if _, err := engine.Run(ctx, task); err == nil {
		t.Fatal("expected resolution failure")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "fLaC" {
		t.Fatalf("unresolvable track moved existing file: %q err=%v", data, err)
	}
	backups, _ := filepath.Glob(path + ".corrupt-*")
	if len(backups) != 0 {
		t.Fatalf("unnecessary backups created: %v", backups)
	}
}

func TestPartialAndFatalErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("individual error continues", func(t *testing.T) {
		p := &fakeProvider{playlist: playlist("1", "2", "3"), resolveErr: map[string]error{"2": errors.New("temporarily unavailable")}}
		engine, _, _, task := setup(t, p)
		result, err := engine.Run(ctx, task)
		if err == nil || result.Status != "partial" || result.Downloaded != 2 || result.Failed != 1 || result.Pending != 0 {
			t.Fatalf("result %+v err=%v", result, err)
		}
	})
	for _, status := range []int{401, 402, 403, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			p := &fakeProvider{playlist: playlist("1", "2", "3"), resolveErr: map[string]error{"1": &chksz.APIError{Status: status, Message: "stop"}}}
			engine, store, _, task := setup(t, p)
			result, err := engine.Run(ctx, task)
			if err == nil || result.Status != "failed" || result.Failed != 1 || result.Pending != 2 || len(p.resolved) != 1 {
				t.Fatalf("result %+v resolves=%v err=%v", result, p.resolved, err)
			}
			runs, _ := store.LatestRuns(ctx, 1)
			if len(runs) != 1 || runs[0].Status != "failed" || runs[0].Pending != 2 {
				t.Fatalf("fatal outcome not persisted: %+v", runs)
			}
		})
	}
}

func TestIncompletePlaylistNeverClaimsSuccess(t *testing.T) {
	p := &fakeProvider{playlist: playlist("1")}
	p.playlist.ReportedCount = 10
	engine, _, _, task := setup(t, p)
	result, err := engine.Run(context.Background(), task)
	if err == nil || result.Status != "partial" || result.Downloaded != 1 || result.ReportedCount != 10 {
		t.Fatalf("incomplete playlist result %+v err=%v", result, err)
	}
}

func TestCancellationRecordsOutcomeAndSerializesTasks(t *testing.T) {
	p := &fakeProvider{playlist: playlist("1"), entered: make(chan struct{}, 1), block: make(chan struct{})}
	engine, store, _, task := setup(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := engine.Run(ctx, task)
		finished <- err
	}()
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first run did not enter provider")
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer waitCancel()
	second, err := engine.Run(waitCtx, task)
	if !errors.Is(err, context.DeadlineExceeded) || second.Status != "canceled" {
		t.Fatalf("waiting run result %+v err=%v", second, err)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("running task did not stop")
	}
	runs, err := store.LatestRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "canceled" || runs[0].FinishedAt == nil {
		t.Fatalf("canceled history %+v err=%v", runs, err)
	}
}

func TestRestartUsesPersistedDownloads(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	p := &fakeProvider{playlist: playlist("1")}
	d := &fakeDownloader{}
	task := model.Task{ID: "persisted", PlaylistID: "123", Directory: filepath.Join(dir, "music")}
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.New(p, d, store).Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	result, err := syncer.New(p, d, store).Run(ctx, task)
	if err != nil || result.Skipped != 1 || d.calls != 1 {
		t.Fatalf("restart result %+v downloads=%d err=%v", result, d.calls, err)
	}
}
