package state

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSharedDownloadIndexMigratesV4WithoutChangingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Download{TaskID: "retained", Provider: "netease", TrackID: "1", Path: "music.flac", Size: 12, SHA256: "stored-digest", Collection: "collection--retained", TagFingerprint: "stored-tags"}
	if err := store.SaveDownload(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	want, _, _ = store.GetDownload(t.Context(), want.TaskID, want.Provider, want.TrackID)
	for _, statement := range []string{`DROP INDEX downloads_provider_track`, `PRAGMA user_version = 4`} {
		if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var version, indexes int
		if err := store.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
			t.Fatalf("shared lookup schema version: %d, %v", version, err)
		}
		if err := store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pragma_index_list('downloads') WHERE name = 'downloads_provider_track'`).Scan(&indexes); err != nil || indexes != 1 {
			t.Fatalf("shared lookup index: %d, %v", indexes, err)
		}
		items, err := store.FindDownloads(t.Context(), want.Provider, want.TrackID)
		if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], want) {
			t.Fatalf("migration changed retained audio record: %+v, %v", items, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
