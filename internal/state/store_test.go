package state_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meloport/internal/state"
)

func TestDownloadsPersistAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "state.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := state.Download{TaskID: "daily", Provider: "netease", TrackID: "1234567890123456789", Path: "song.flac", Size: 321,
		SHA256: "abc", Title: "测试歌曲", Artists: []string{"甲", "乙"}, Album: "专辑"}
	if err := store.SaveDownload(ctx, want); err != nil {
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
	got, found, err := store.GetDownload(ctx, want.TaskID, want.Provider, want.TrackID)
	if err != nil || !found {
		t.Fatalf("GetDownload found=%v err=%v", found, err)
	}
	if got.Path != want.Path || got.Size != want.Size || got.Title != want.Title || len(got.Artists) != 2 || got.Artists[1] != "乙" || got.UpdatedAt.IsZero() {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if _, found, err := store.GetDownload(ctx, "different-task", want.Provider, want.TrackID); err != nil || found {
		t.Fatalf("task isolation found=%v err=%v", found, err)
	}
	want.Size = 555
	if err := store.SaveDownload(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, _, err = store.GetDownload(ctx, want.TaskID, want.Provider, want.TrackID)
	if err != nil || got.Size != 555 {
		t.Fatalf("upsert got=%+v err=%v", got, err)
	}
}

func TestRunHistoryReadOnlyAndExplicitRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.StartRun(ctx, "complete")
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	if err := store.FinishRun(ctx, state.Run{ID: first, Status: "success", FinishedAt: &finished, PlaylistName: "喜欢", Total: 4, Downloaded: 3, Skipped: 1}); err != nil {
		t.Fatal(err)
	}
	second, err := store.StartRun(ctx, "interrupted")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := state.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	runs, err := reader.LatestRuns(ctx, 1)
	if err != nil || len(runs) != 1 || runs[0].ID != second || runs[0].Status != "running" {
		t.Fatalf("read only changed running state or bad history: runs=%+v err=%v", runs, err)
	}
	if _, err := reader.StartRun(ctx, "must-not-write"); err == nil {
		t.Fatal("read-only store allowed a write")
	}
	if err := store.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	runs, err = reader.LatestRuns(ctx, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("LatestRuns: %+v %v", runs, err)
	}
	if runs[0].Status != "interrupted" || runs[0].FinishedAt == nil || runs[0].Error == "" {
		t.Fatalf("incomplete recovery: %+v", runs[0])
	}
	if runs[1].Status != "success" || runs[1].Downloaded != 3 || runs[1].Skipped != 1 || runs[1].PlaylistName != "喜欢" {
		t.Fatalf("finished run was changed: %+v", runs[1])
	}
}

func TestOpenReadOnlyMissingDoesNotCreate(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "absent")
	if store, err := state.OpenReadOnly(filepath.Join(parent, "state.db")); err == nil {
		store.Close()
		t.Fatal("expected missing read-only database to fail")
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("read-only open created a directory: %v", err)
	}
}

func TestInvalidRecordsAndCanceledQueries(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveDownload(context.Background(), state.Download{}); err == nil {
		t.Fatal("accepted incomplete record")
	}
	if err := store.FinishRun(context.Background(), state.Run{ID: 100, Status: "success"}); err == nil {
		t.Fatal("accepted missing run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Ping(ctx); err == nil {
		t.Fatal("ignored canceled database context")
	}
}
