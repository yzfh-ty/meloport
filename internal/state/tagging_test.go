package state_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"meloport/internal/model"
	"meloport/internal/state"
)

func TestTagStatePersistsAndLocalListingIsIsolated(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := state.Download{TaskID: "daily", Provider: "netease", TrackID: "123", Path: "music/song.flac", Size: 123,
		SHA256: "digest", Title: "Song", Artists: []string{"Artist"}, Album: "Album", TagFingerprint: "tags-v1",
		Metadata: model.Track{ID: "123", Name: "Song", Artists: []string{"Artist"}, Album: "Album"}}
	if err := store.SaveDownload(ctx, want); err != nil {
		t.Fatal(err)
	}
	other := want
	other.TaskID = "different-task"
	if err := store.SaveDownload(ctx, other); err != nil {
		t.Fatal(err)
	}
	other.TaskID, other.Provider = "daily", "qq"
	if err := store.SaveDownload(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginTag(ctx, want); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	items, err := store.ListDownloads(ctx, "daily", "netease")
	if err != nil || len(items) != 1 || !items[0].TagPending || items[0].TagFingerprint != "tags-v1" || items[0].Metadata.Name != "Song" {
		t.Fatalf("persisted tag state %+v err=%v", items, err)
	}
	item := items[0]
	item.TagPending = false
	item.Size = 456
	if err := store.SaveDownload(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.GetDownload(ctx, "daily", "netease", "123")
	if err != nil || got.TagPending || got.Size != 456 || got.Metadata.Album != "Album" {
		t.Fatalf("completed tag state %+v err=%v", got, err)
	}
	want.Path = "different-file.flac"
	if err := store.BeginTag(ctx, want); err == nil {
		t.Fatal("pending update accepted a different file")
	}
}

func TestSchemaV1MigratesExistingDownloadsWithoutChangingAudioRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`CREATE TABLE downloads (task_id TEXT NOT NULL, provider TEXT NOT NULL, track_id TEXT NOT NULL,
		path TEXT NOT NULL, size INTEGER NOT NULL, sha256 TEXT NOT NULL, title TEXT NOT NULL,
		artists TEXT NOT NULL, album TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(task_id, provider, track_id))`,
		`INSERT INTO downloads VALUES ('old', 'netease', '1', 'song.flac', 42, 'digest', 'Old title', '["Artist"]', 'Album', '2026-01-01T00:00:00Z')`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, found, err := store.GetDownload(ctx, "old", "netease", "1")
	if err != nil || !found || got.Title != "Old title" || got.Size != 42 || got.SHA256 != "digest" || got.TagFingerprint != "" || got.TagPending {
		t.Fatalf("migrated old download %+v found=%v err=%v", got, found, err)
	}
	if err := store.BeginTag(ctx, got); err != nil {
		t.Fatal(err)
	}
}
