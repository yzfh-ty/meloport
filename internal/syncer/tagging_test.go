package syncer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"meloport/internal/model"
	"meloport/internal/syncer"
)

type taggingDownloader struct {
	fakeDownloader
	tags      int
	err       error
	tracks    []model.Track
	warning   string
	unchanged bool
}

func (d *taggingDownloader) Download(ctx context.Context, audio model.ResolvedTrack, directory, basename string) (model.DownloadResult, error) {
	result, err := d.fakeDownloader.Download(ctx, audio, directory, basename)
	if err != nil {
		return result, err
	}
	return d.Tag(ctx, audio.Track, result.Path)
}

func (d *taggingDownloader) Tag(ctx context.Context, track model.Track, path string) (model.DownloadResult, error) {
	if err := ctx.Err(); err != nil {
		return model.DownloadResult{}, err
	}
	d.tags++
	d.tracks = append(d.tracks, track)
	if d.err != nil {
		return model.DownloadResult{}, d.err
	}
	// Real taggers rewrite metadata atomically; changing size here exercises the
	// state bookkeeping that prevents a later sync from re-downloading audio.
	data, err := os.ReadFile(path)
	if err != nil {
		return model.DownloadResult{}, err
	}
	if !d.unchanged {
		data = append(data, []byte(" tagged "+track.Name)...)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return model.DownloadResult{}, err
	}
	hash := sha256.Sum256(data)
	return model.DownloadResult{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), TagsComplete: true, TagWarning: d.warning}, nil
}

func TestNewDownloadsCarryTagsAndUnchangedRunsDoNotRetag(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1")}
	_, store, _, task := setup(t, p)
	d := &taggingDownloader{}
	engine := syncer.New(p, d, store)
	first, err := engine.Run(ctx, task)
	if err != nil || first.Downloaded != 1 || d.tags != 1 || d.tracks[0].Name != "Song 1" {
		t.Fatalf("new tags %+v tags=%d tracks=%+v err=%v", first, d.tags, d.tracks, err)
	}
	item, _, err := store.GetDownload(ctx, task.ID, "netease", "1")
	if err != nil || item.Metadata.Name != "Song 1" || item.TagFingerprint == "" || item.TagPending {
		t.Fatalf("tagged record %+v err=%v", item, err)
	}
	second, err := engine.Run(ctx, task)
	if err != nil || second.Skipped != 1 || d.tags != 1 || d.calls != 1 || len(p.resolved) != 1 {
		t.Fatalf("unchanged tags %+v tags=%d downloads=%d resolves=%v err=%v", second, d.tags, d.calls, p.resolved, err)
	}
	p.playlist.Tracks[0].Name = "Corrected title"
	third, err := engine.Run(ctx, task)
	item, _, _ = store.GetDownload(ctx, task.ID, "netease", "1")
	info, statErr := os.Stat(item.Path)
	if err != nil || third.Skipped != 1 || d.tags != 2 || d.calls != 1 || len(p.resolved) != 1 || item.Title != "Corrected title" || statErr != nil || item.Size != info.Size() {
		t.Fatalf("changed metadata %+v record=%+v tags=%d err=%v stat=%v", third, item, d.tags, err, statErr)
	}
}

func TestOldDownloadGetsTagsWithoutAudioResolution(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1")}
	engine, store, oldDownloader, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	d := &taggingDownloader{}
	result, err := syncer.New(p, d, store).Run(ctx, task)
	if err != nil || result.Skipped != 1 || d.tags != 1 || d.calls != 0 || oldDownloader.calls != 1 || len(p.resolved) != 1 {
		t.Fatalf("old tags %+v tags=%d resolves=%v err=%v", result, d.tags, p.resolved, err)
	}
}

func TestTagFailureNeverResolvesAudioAndIsRetried(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1")}
	engine, store, _, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	item, _, _ := store.GetDownload(ctx, task.ID, "netease", "1")
	before, _ := os.ReadFile(item.Path)
	d := &taggingDownloader{err: errors.New("tag update failed")}
	engine = syncer.New(p, d, store)
	result, err := engine.Run(ctx, task)
	got, _, _ := store.GetDownload(ctx, task.ID, "netease", "1")
	after, _ := os.ReadFile(item.Path)
	if err == nil || result.Failed != 1 || result.Status != "failed" || got.TagPending || d.calls != 0 || len(p.resolved) != 1 || string(before) != string(after) {
		t.Fatalf("failed tags %+v record=%+v resolves=%v err=%v", result, got, p.resolved, err)
	}
	d.err = nil
	result, err = engine.Run(ctx, task)
	got, _, _ = store.GetDownload(ctx, task.ID, "netease", "1")
	if err != nil || result.Skipped != 1 || got.TagPending || d.calls != 0 || len(p.resolved) != 1 {
		t.Fatalf("tag retry %+v record=%+v err=%v", result, got, err)
	}
}

func TestPendingTagRecoveryDoesNotQuarantineChangedSize(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1")}
	engine, store, _, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	item, _, _ := store.GetDownload(ctx, task.ID, "netease", "1")
	if err := store.BeginTag(ctx, item); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(item.Path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(" metadata written before process exited")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	d := &taggingDownloader{}
	result, err := syncer.New(p, d, store).Run(ctx, task)
	if err != nil || result.Skipped != 1 || d.tags != 1 || d.calls != 0 || len(p.resolved) != 1 {
		t.Fatalf("pending recovery %+v tags=%d err=%v", result, d.tags, err)
	}
	backups, _ := filepath.Glob(item.Path + ".corrupt-*")
	if len(backups) != 0 {
		t.Fatalf("tag update quarantined: %v", backups)
	}
}

func TestRetagUsesOnlyLocalRecordsIncludingRemovedSongs(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1", "2")}
	engine, store, _, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	p.playlist = playlist("1")
	p.playlistErr = errors.New("upstream must not be queried")
	d := &taggingDownloader{}
	result, err := syncer.New(p, d, store).Retag(ctx, task)
	if err != nil || result.Total != 2 || result.Tagged != 2 || result.Failed != 0 || d.calls != 0 || d.tags != 2 || len(p.resolved) != 2 {
		t.Fatalf("local retag %+v tags=%d resolves=%v err=%v", result, d.tags, p.resolved, err)
	}
	item, _, _ := store.GetDownload(ctx, task.ID, "netease", "2")
	if item.Metadata.Name != "Song 2" || item.TagFingerprint == "" || item.TagPending {
		t.Fatalf("removed track metadata not retained: %+v", item)
	}
}

func TestRetagMissingOrMovedFilesDoesNotDownload(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1", "2")}
	engine, store, _, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(task.Directory, "netease-1.flac")); err != nil {
		t.Fatal(err)
	}
	d := &taggingDownloader{}
	engine = syncer.New(p, d, store)
	result, err := engine.Retag(ctx, task)
	if err == nil || result.Failed != 1 || result.Tagged != 1 || d.calls != 0 || len(p.resolved) != 2 {
		t.Fatalf("missing retag %+v resolves=%v err=%v", result, p.resolved, err)
	}
	task.Directory = filepath.Join(filepath.Dir(task.Directory), "new-directory")
	result, err = engine.Retag(ctx, task)
	if err == nil || result.Failed != 2 || result.Tagged != 0 || d.tags != 1 || d.calls != 0 {
		t.Fatalf("moved directory retag %+v tags=%d err=%v", result, d.tags, err)
	}
}

func TestRetagWithoutTaggerIsExplicitError(t *testing.T) {
	p := &fakeProvider{playlist: playlist("1")}
	engine, _, _, task := setup(t, p)
	if _, err := engine.Retag(context.Background(), task); err == nil {
		t.Fatal("untagged downloader reported successful local repair")
	}
}

func TestTagWarningDoesNotFailDownloadOrRepeatUnchangedTags(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1")}
	_, store, _, task := setup(t, p)
	d := &taggingDownloader{warning: "cover could not be embedded"}
	engine := syncer.New(p, d, store)
	result, err := engine.Run(ctx, task)
	if err != nil || result.Status != "success" || result.Downloaded != 1 || len(result.TagWarnings) != 1 {
		t.Fatalf("download warning %+v err=%v", result, err)
	}
	result, err = engine.Run(ctx, task)
	if err != nil || result.Skipped != 1 || d.tags != 1 || len(result.TagWarnings) != 0 {
		t.Fatalf("warning retried unnecessarily %+v tags=%d err=%v", result, d.tags, err)
	}
	d.unchanged = true
	retagged, err := engine.Retag(ctx, task)
	if err != nil || retagged.Skipped != 1 || retagged.Tagged != 0 || len(retagged.TagWarnings) != 1 || d.tags != 2 {
		t.Fatalf("explicit cover retry %+v tags=%d err=%v", retagged, d.tags, err)
	}
}

func TestRetagKeepsSharedPathRecordsConsistent(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{playlist: playlist("1")}
	engine, store, _, task := setup(t, p)
	if _, err := engine.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	item, _, err := store.GetDownload(ctx, task.ID, "netease", "1")
	if err != nil {
		t.Fatal(err)
	}
	legacyTask := task
	legacyTask.ID = "former-task"
	alias := item
	alias.TaskID = legacyTask.ID
	alias.TagFingerprint = "previous fingerprint"
	if err := store.SaveDownload(ctx, alias); err != nil {
		t.Fatal(err)
	}
	d := &taggingDownloader{}
	engine = syncer.New(p, d, store)
	if _, err := engine.Retag(ctx, task); err != nil {
		t.Fatal(err)
	}
	newItem, _, err := store.GetDownload(ctx, task.ID, "netease", "1")
	if err != nil {
		t.Fatal(err)
	}
	newAlias, _, err := store.GetDownload(ctx, legacyTask.ID, "netease", "1")
	if err != nil || newItem.Size == item.Size || newAlias.Size != newItem.Size || newAlias.SHA256 != newItem.SHA256 || newAlias.TagPending || newAlias.TagFingerprint != "" {
		t.Fatalf("shared file not synchronized: edited=%+v alias=%+v err=%v", newItem, newAlias, err)
	}
	d.unchanged = true
	result, err := engine.Run(ctx, legacyTask)
	if err != nil || result.Skipped != 1 || d.calls != 0 || len(p.resolved) != 1 {
		t.Fatalf("shared record redispatched audio: %+v err=%v", result, err)
	}
}

var _ syncer.Tagger = (*taggingDownloader)(nil)
