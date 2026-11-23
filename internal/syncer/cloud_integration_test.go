package syncer_test

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"meloport/internal/cloud115"
	"meloport/internal/cloudsync"
	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

type cloudUploadFixture map[string][]cloud115.File

func (f cloudUploadFixture) List(_ context.Context, cid string) ([]cloud115.File, error) {
	files, ok := f[cid]
	if !ok {
		return nil, fmt.Errorf("unexpected 115 directory %s", cid)
	}
	return files, nil
}

type cloudDownloadFixture struct{ calls int }

func (d *cloudDownloadFixture) Download(ctx context.Context, audio model.ResolvedTrack, directory, basename string) (model.DownloadResult, error) {
	if err := ctx.Err(); err != nil {
		return model.DownloadResult{}, err
	}
	d.calls++
	path := filepath.Join(directory, basename+".flac")
	data := []byte("fLaC audio fixture for " + audio.Track.ID)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return model.DownloadResult{}, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return model.DownloadResult{}, err
	}
	return model.DownloadResult{
		Path: path, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
		TagsComplete: audio.Track.ID == "1",
	}, nil
}

func TestCloudSyncSurvivesCloudDrive2CleanupAndRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database := filepath.Join(root, "cloud.db")
	const rootCID, taskCID = "100", "200"
	task := model.Task{ID: "favorites", Platform: "netease", PlaylistID: "123", Quality: "lossless", Directory: filepath.Join(root, "work")}
	collection := model.CollectionFolder(task.ID, "收藏")
	opts := cloudsync.Options{
		TargetCID: rootCID, MountDir: filepath.Join(root, "mounted-115"), STRMDir: filepath.Join(root, "strm"),
		MediaBaseURL: "https://music.example.test",
	}
	if err := os.Mkdir(opts.MountDir, 0o750); err != nil {
		t.Fatal(err)
	}
	remote := cloudUploadFixture{
		rootCID: {{ID: "150", ParentID: rootCID, Name: task.Platform, IsDir: true}},
		"150":   {{ID: taskCID, ParentID: "150", Name: collection, IsDir: true}},
		taskCID: nil,
	}
	provider := &fakeProvider{playlist: playlist("1", "2")}
	downloader := &cloudDownloadFixture{}
	open := func() (*state.Store, *cloudsync.Service, *syncer.Engine) {
		t.Helper()
		store, err := state.Open(database)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		service := cloudsync.New(store, remote, opts)
		engine := syncer.New(provider, downloader, store)
		engine.UseCloudStorage(service)
		return store, service, engine
	}
	store, service, engine := open()
	first, err := engine.Run(ctx, task)
	if err == nil || first.Status != "partial" || first.Downloaded != 1 || first.Failed != 1 {
		t.Fatalf("first sync = %+v, %v", first, err)
	}
	const readyName, incompleteName = "netease-1.flac", "netease-2.flac"
	stagedPath := filepath.Join(opts.MountDir, task.Platform, collection, readyName)
	staged, err := os.ReadFile(stagedPath)
	if err != nil || string(staged) != "fLaC audio fixture for 1" {
		t.Fatalf("read staged audio = %q, %v", staged, err)
	}
	if _, err := os.Stat(filepath.Join(task.Directory, readyName)); err != nil {
		t.Fatal("local download was not retained", err)
	}
	assertCloudTestPathMissing(t, filepath.Join(opts.MountDir, task.Platform, collection, incompleteName))
	if _, err := os.Stat(filepath.Join(task.Directory, incompleteName)); err != nil {
		t.Fatalf("audio with unfinished tags was not retained in the working directory: %v", err)
	}
	if _, exists, err := store.GetCloudFile(ctx, rootCID, task.ID, "netease", "2"); err != nil || exists {
		t.Fatalf("unexpected CloudDrive2 handoff for audio with unfinished tags: exists=%v, %v", exists, err)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	strmPath := filepath.Join(opts.STRMDir, task.Platform, collection, readyName+".strm")
	assertCloudTestPathMissing(t, strmPath)
	provider.playlist = playlist("1")
	assertSkipped := func(who string) {
		t.Helper()
		run, err := engine.Run(ctx, task)
		if err != nil || run.Skipped != 1 || run.Downloaded != 0 || downloader.calls != 2 || len(provider.resolved) != 2 {
			t.Fatalf("%s = %+v, %v, downloads=%d resolves=%v", who, run, err, downloader.calls, provider.resolved)
		}
	}
	assertSkipped("waiting for CloudDrive2")
	remote[taskCID] = []cloud115.File{{
		ID: "300", ParentID: taskCID, Name: readyName, PickCode: "fixtureABC", Size: int64(len(staged)),
		SHA1: fmt.Sprintf("%x", sha1.Sum(staged)),
	}}
	// CloudDrive2 finished uploading and deleted the source before Meloport polled 115.
	if err := os.Remove(stagedPath); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, service, engine = open()
	assertSkipped("restarted before 115 confirmation")
	assertCloudTestPathMissing(t, stagedPath)
	assertCloudTestPathMissing(t, strmPath)
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	confirmed, exists, err := store.GetCloudFile(ctx, rootCID, task.ID, "netease", "1")
	if err != nil || !exists || confirmed.CID != taskCID || confirmed.ID != "300" || confirmed.PickCode != "fixtureABC" || confirmed.MediaID == "" {
		t.Fatalf("confirmed 115 mapping = %+v, %v, %v", confirmed, exists, err)
	}
	wantSTRM := opts.MediaBaseURL + "/media/" + confirmed.MediaID + "\n"
	if body, err := os.ReadFile(strmPath); err != nil || string(body) != wantSTRM {
		t.Fatalf("STRM = %q, %v", body, err)
	}
	if err := os.Remove(strmPath); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// The persisted mapping can regenerate a missing export while 115 is unavailable.
	delete(remote, rootCID)
	store, service, engine = open()
	assertSkipped("restarted after 115 confirmation")
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(strmPath); err != nil || string(body) != wantSTRM {
		t.Fatalf("repaired STRM = %q, %v", body, err)
	}
}

func assertCloudTestPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent, stat error: %v", path, err)
	}
}
