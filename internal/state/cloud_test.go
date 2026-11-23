package state

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCloudMigrationFromV3PreservesState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	download := Download{TaskID: "old", Provider: "netease", TrackID: "1", Path: "old.flac", Size: 14, TagFingerprint: "tags-v3", TagPending: true}
	if err := store.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	wantDownload, _, err := store.GetDownload(ctx, download.TaskID, download.Provider, download.TrackID)
	if err != nil {
		t.Fatal(err)
	}
	cloud, err := store.SaveCloudIntent(ctx, CloudFile{
		RootCID: "123", TaskID: download.TaskID, Provider: download.Provider, TrackID: download.TrackID,
		LocalPath: download.Path, TargetPath: "staging/old.flac", RelativePath: "old.flac",
		Size: download.Size, SHA1: "0123456789012345678901234567890123456789", MediaID: "existing-media",
	})
	if err != nil {
		t.Fatal(err)
	}
	cloud.CID, cloud.ID, cloud.PickCode = "456", "789", "existing-pick-code"
	cloud.Published = true
	if err := store.ConfirmCloudFile(ctx, cloud); err != nil {
		t.Fatal(err)
	}
	runID, err := store.StartRun(ctx, download.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, Run{ID: runID, Status: "success", Downloaded: 1}); err != nil {
		t.Fatal(err)
	}
	wantRuns, err := store.LatestRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the v3 schema: cloud state exists, chart folder state does not.
	for _, query := range []string{`ALTER TABLE downloads DROP COLUMN collection`, `PRAGMA user_version = 3`} {
		if _, err := store.db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Verify both the upgrade and a subsequent ordinary startup.
	for attempt := 0; attempt < 2; attempt++ {
		migrated, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = migrated.Close() })
		var version int
		if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
			t.Fatalf("database version = %d, err = %v", version, err)
		}
		got, found, err := migrated.GetDownload(ctx, download.TaskID, download.Provider, download.TrackID)
		if err != nil || !found || !reflect.DeepEqual(got, wantDownload) {
			t.Fatalf("download changed: got=%+v found=%t err=%v", got, found, err)
		}
		gotCloud, found, err := migrated.GetCloudByMediaID(ctx, cloud.MediaID)
		if err != nil || !found || gotCloud != cloud {
			t.Fatalf("cloud mapping changed: got=%+v found=%t err=%v", gotCloud, found, err)
		}
		gotRuns, err := migrated.LatestRuns(ctx, 10)
		if err != nil || !reflect.DeepEqual(gotRuns, wantRuns) {
			t.Fatalf("run history changed: got=%+v err=%v", gotRuns, err)
		}
		if err := migrated.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

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
