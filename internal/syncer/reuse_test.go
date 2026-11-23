package syncer_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meloport/internal/cloudsync"
	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

func TestSubscriptionsReuseExistingAudioAndPublishTheirOwnRecords(t *testing.T) {
	p := &fakeProvider{playlist: playlist("1", "2", "2")}
	engine, store, downloader, original := setup(t, p)
	if _, err := engine.Run(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	// The resolver must not be consulted when all audio already exists locally.
	p.resolveErr = map[string]error{"1": errors.New("no API key"), "2": errors.New("no API key")}
	next := original
	next.ID, next.Directory, next.Quality = "manual-subscription", filepath.Join(filepath.Dir(original.Directory), "second"), "hires"
	publisher := &playlistPublicationFixture{}
	engine.UsePlaylistPublisher(publisher)
	result, err := engine.Run(t.Context(), next)
	if err != nil || result.Status != "success" || result.Skipped != 2 || result.Downloaded != 0 || result.Pending != 0 || downloader.calls != 2 || len(p.resolved) != 2 || publisher.calls != 1 {
		t.Fatalf("cross-subscription reuse: %+v, %v, downloads=%d, resolves=%v, publications=%d", result, err, downloader.calls, p.resolved, publisher.calls)
	}
	for _, id := range []string{"1", "2"} {
		source, _, _ := store.GetDownload(t.Context(), original.ID, "netease", id)
		target, found, err := store.GetDownload(t.Context(), next.ID, "netease", id)
		if err != nil || !found || target.Path != filepath.Join(next.Directory, "netease-"+id+".flac") || target.SHA256 != source.SHA256 || target.Collection == source.Collection {
			t.Fatalf("new subscription record: %+v, found=%t, err=%v", target, found, err)
		}
		before, _ := os.ReadFile(source.Path)
		after, _ := os.ReadFile(target.Path)
		if !bytes.Equal(before, after) {
			t.Fatal("local reuse changed audio bytes")
		}
		if err := os.WriteFile(target.Path, []byte("destination edited independently"), 0600); err != nil {
			t.Fatal(err)
		}
		unchanged, _ := os.ReadFile(source.Path)
		if !bytes.Equal(before, unchanged) {
			t.Fatal("reuse coupled the original and new subscription files")
		}
	}
	assertNoReuseTemps(t, next.Directory)
}

func TestLocalReuseRepairsTruncatedDestinationWithoutResolvingAudio(t *testing.T) {
	p := &fakeProvider{playlist: playlist("1")}
	engine, store, downloader, source := setup(t, p)
	if _, err := engine.Run(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	task := source
	task.ID, task.Directory = "second", filepath.Join(filepath.Dir(source.Directory), "second")
	if _, err := engine.Run(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	item, _, _ := store.GetDownload(t.Context(), task.ID, "netease", "1")
	if err := os.WriteFile(item.Path, []byte("fLaC"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), task)
	if err != nil || result.Skipped != 1 || result.Downloaded != 0 || downloader.calls != 1 || len(p.resolved) != 1 {
		t.Fatalf("reuse repair: %+v, %v, downloads=%d, resolves=%v", result, err, downloader.calls, p.resolved)
	}
	backups, err := filepath.Glob(item.Path + ".corrupt-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("truncated file was not preserved: %v, %v", backups, err)
	}
	data, _ := os.ReadFile(backups[0])
	if string(data) != "fLaC" {
		t.Fatal("backup did not retain the truncated file")
	}
	assertNoReuseTemps(t, task.Directory)
}

func TestConcurrentSubscriptionsDownloadSharedSongOnce(t *testing.T) {
	p := &fakeProvider{playlist: playlist("1")}
	engine, _, downloader, first := setup(t, p)
	second := first
	second.ID, second.Directory = "second", filepath.Join(filepath.Dir(first.Directory), "second")
	type outcome struct {
		summary syncer.Summary
		err     error
	}
	results := make(chan outcome, 2)
	for _, task := range []model.Task{first, second} {
		go func() {
			summary, err := engine.Run(t.Context(), task)
			results <- outcome{summary, err}
		}()
	}
	downloaded, skipped := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.err != nil || result.summary.Status != "success" {
				t.Fatalf("concurrent subscription: %+v, %v", result.summary, result.err)
			}
			downloaded += result.summary.Downloaded
			skipped += result.summary.Skipped
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent subscriptions did not finish")
		}
	}
	if downloaded != 1 || skipped != 1 || downloader.calls != 1 || len(p.resolved) != 1 {
		t.Fatalf("shared song resolved or downloaded twice: downloaded=%d skipped=%d calls=%d resolves=%v", downloaded, skipped, downloader.calls, p.resolved)
	}
}

func TestLocalReuseRejectsInvalidSourcesAndConflictingVersions(t *testing.T) {
	for _, name := range []string{"missing", "same-size corruption", "symlink", "pending tags", "other platform", "other ID", "live title", "other artist", "other album", "other hash", "other duration"} {
		t.Run(name, func(t *testing.T) {
			p := &fakeProvider{playlist: playlist("1")}
			p.playlist.Tracks[0].Album = "Album"
			p.playlist.Tracks[0].Hash = "AABB"
			p.playlist.Tracks[0].Duration = 200
			engine, store, downloader, task := setup(t, p)
			item := saveReuseSource(t, store, task, p.playlist.Tracks[0])
			switch name {
			case "missing":
				if err := os.Remove(item.Path); err != nil {
					t.Fatal(err)
				}
			case "same-size corruption":
				data, _ := os.ReadFile(item.Path)
				data[len(data)-1] ^= 1
				if err := os.WriteFile(item.Path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(item.Path, item.Path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(item.Path+".original", item.Path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "pending tags":
				if err := store.BeginTag(t.Context(), item); err != nil {
					t.Fatal(err)
				}
			case "other platform":
				item.Provider = "qq"
			case "other ID":
				item.TrackID = "2"
			case "live title":
				item.Metadata.Name += " (Live)"
			case "other artist":
				item.Metadata.Artists = []string{"Cover singer"}
			case "other album":
				item.Metadata.Album = "Live Album"
			case "other hash":
				item.Metadata.Hash = "CCDD"
			case "other duration":
				item.Metadata.Duration = 240
			}
			if name == "other platform" || name == "other ID" {
				// Remove the matching original alias by moving its native identity
				// in a separate store, rather than retaining it as a valid candidate.
				other, err := state.Open(filepath.Join(t.TempDir(), "other.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = other.Close() })
				store = other
				engine = syncer.New(p, downloader, store)
			}
			if name != "pending tags" {
				if err := store.SaveDownload(t.Context(), item); err != nil {
					t.Fatal(err)
				}
			}
			result, err := engine.Run(t.Context(), task)
			if err != nil || result.Downloaded != 1 || result.Skipped != 0 || downloader.calls != 1 || len(p.resolved) != 1 {
				t.Fatalf("invalid source was reused: %+v, %v, downloads=%d, resolves=%v", result, err, downloader.calls, p.resolved)
			}
			assertNoReuseTemps(t, task.Directory)
		})
	}
}

func TestLocalReuseTriesOlderValidRecordsAndPreservesUnknownDestination(t *testing.T) {
	p := &fakeProvider{playlist: playlist("1")}
	engine, store, downloader, task := setup(t, p)
	item := saveReuseSource(t, store, task, p.playlist.Tracks[0])
	newest := item
	newest.TaskID, newest.Path, newest.UpdatedAt = "newer-missing", item.Path+".missing.flac", time.Now().Add(time.Hour)
	if err := store.SaveDownload(t.Context(), newest); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(task.Directory, 0750); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(task.Directory, "netease-1.flac")
	unknown := []byte("unregistered user audio")
	if err := os.WriteFile(destination, unknown, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), task)
	if err == nil || result.Failed != 1 || downloader.calls != 0 || len(p.resolved) != 0 {
		t.Fatalf("unknown destination was not preserved: %+v, %v", result, err)
	}
	after, _ := os.ReadFile(destination)
	if !bytes.Equal(after, unknown) {
		t.Fatal("unknown destination was overwritten")
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	result, err = engine.Run(t.Context(), task)
	if err != nil || result.Skipped != 1 || downloader.calls != 0 || len(p.resolved) != 0 {
		t.Fatalf("older valid download was not reused: %+v, %v", result, err)
	}
	assertNoReuseTemps(t, task.Directory)
}

func TestReusedAudioCanMoveToCloudDrive2WithoutMovingItsSource(t *testing.T) {
	p := &fakeProvider{playlist: playlist("1")}
	_, store, _, task := setup(t, p)
	downloader := &cloudDownloadFixture{}
	engine := syncer.New(p, downloader, store)
	if _, err := engine.Run(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	original, _, _ := store.GetDownload(t.Context(), task.ID, "netease", "1")
	opts := cloudsync.Options{TargetCID: "12345", MountDir: filepath.Join(filepath.Dir(task.Directory), "staging"), STRMDir: filepath.Join(filepath.Dir(task.Directory), "strm"), MediaBaseURL: "https://media.example.test"}
	if err := os.Mkdir(opts.MountDir, 0o750); err != nil {
		t.Fatal(err)
	}
	engine.UseCloudStorage(cloudsync.New(store, cloudUploadFixture{}, opts))
	next := task
	next.ID, next.Directory = "new-cloud", filepath.Join(filepath.Dir(task.Directory), "new-cloud")
	result, err := engine.Run(t.Context(), next)
	if err != nil || result.Skipped != 1 || result.Downloaded != 0 || downloader.calls != 1 || len(p.resolved) != 1 {
		t.Fatalf("reused CloudDrive2 handoff: %+v, %v", result, err)
	}
	cloud, found, err := store.GetCloudFile(t.Context(), opts.TargetCID, next.ID, "netease", "1")
	if err != nil || !found || !cloud.Published {
		t.Fatalf("reused cloud mapping missing: %+v, found=%t, err=%v", cloud, found, err)
	}
	before, err := os.ReadFile(original.Path)
	if err != nil {
		t.Fatal("CloudDrive2 reuse moved the source subscription file")
	}
	after, err := os.ReadFile(cloud.TargetPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("CloudDrive2 did not receive the reused audio")
	}
}

func saveReuseSource(t *testing.T, store *state.Store, task model.Task, track model.Track) state.Download {
	t.Helper()
	path := filepath.Join(filepath.Dir(task.Directory), "deleted-subscription", "netease-"+track.ID+".flac")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	data := []byte("fLaCcomplete recorded fixture")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	item := state.Download{TaskID: "deleted-subscription", Provider: "netease", TrackID: track.ID, Path: path, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Metadata: track}
	if err := store.SaveDownload(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	return item
}

func assertNoReuseTemps(t *testing.T, directory string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, ".*-reuse-*.part"))
	if err != nil || len(files) > 0 {
		t.Fatalf("reuse temporary files leaked: %v, %v", files, err)
	}
}
