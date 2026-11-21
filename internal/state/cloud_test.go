package state

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCloudMigrationPreservesV2Tags(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item := Download{TaskID: "old", Provider: "netease", TrackID: "1", Path: "old.flac", Size: 14, TagFingerprint: "tags-v2", TagPending: true}
	if err := store.SaveDownload(ctx, item); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DROP TABLE cloud_files`, `PRAGMA user_version = 2`} {
		if _, err := store.db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got, found, err := migrated.GetDownload(ctx, item.TaskID, item.Provider, item.TrackID)
	if err != nil || !found || got.Path != item.Path || got.TagFingerprint != item.TagFingerprint || !got.TagPending {
		t.Fatalf("existing tagged download was changed: found=%t err=%v", found, err)
	}
	if items, err := migrated.ListCloudFiles(ctx, "123"); err != nil || len(items) != 0 {
		t.Fatalf("migrated cloud table should be empty: got=%d err=%v", len(items), err)
	}
}
