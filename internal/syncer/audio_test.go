package syncer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.senan.xyz/taglib"

	"meloport/internal/download"
	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

type audioMetadataFixture struct {
	entered chan struct{}
	resume  chan struct{}
}

func (s audioMetadataFixture) Playlist(context.Context, string, string) (model.Playlist, error) {
	return model.Playlist{}, errors.New("unexpected playlist request")
}

func (s audioMetadataFixture) TrackMetadata(ctx context.Context, platform string, track model.Track) (model.Track, error) {
	if platform != "qq" || track.ID != "003Local" {
		return model.Track{}, errors.New("unexpected native identity")
	}
	close(s.entered)
	select {
	case <-s.resume:
		return model.Track{ID: track.ID, Name: "平台歌名", Album: "平台专辑"}, nil
	case <-ctx.Done():
		return model.Track{}, ctx.Err()
	}
}

func (s audioMetadataFixture) Lyrics(_ context.Context, platform string, track model.Track) (string, error) {
	if platform != "qq" || track.ID != "003Local" {
		return "", errors.New("unexpected lyric identity")
	}
	return fixtureLRC, nil
}

func TestInspectAndRetagOneAudioFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	file := filepath.Join(dir, "qq-003Local.flac")
	data, err := os.ReadFile(filepath.Join("..", "download", "testdata", "tone.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := taglib.WriteTags(file, map[string][]string{taglib.Title: {"本地歌名"}}, 0); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	task := model.Task{ID: "test", Platform: "qq", Directory: dir}
	item := state.Download{TaskID: task.ID, Provider: task.Platform, TrackID: "003Local", Path: file,
		Size: int64(len(original)), SHA256: hex.EncodeToString(digest[:]), Title: "登记歌名", Artists: []string{"登记歌手"}}
	if err := store.SaveDownload(ctx, item); err != nil {
		t.Fatal(err)
	}
	reader := audioMetadataFixture{entered: make(chan struct{}), resume: make(chan struct{})}
	downloader, err := download.New(download.Options{Lyrics: reader})
	if err != nil {
		t.Fatal(err)
	}
	engine := syncer.NewWithSources(reader, nil, downloader, store)
	view := engine.InspectAudio(ctx, task, item)
	if view.Status != "incomplete" || !view.Retryable || view.Title != "本地歌名" || !slices.Equal(view.Missing, []string{"artist", "album", "cover", "lyrics"}) {
		t.Fatalf("wrong initial file tags: %+v", view)
	}
	otherDir := task
	otherDir.Directory = filepath.Join(dir, "moved")
	if _, err := engine.RetagAudio(ctx, otherDir, item.TrackID); !errors.Is(err, syncer.ErrAudioUnretryable) {
		t.Fatalf("moved directory retag: %v", err)
	}
	done := make(chan error, 1)
	var result syncer.AudioRetagResult
	go func() {
		var err error
		result, err = engine.RetagAudio(ctx, task, item.TrackID)
		done <- err
	}()
	select {
	case <-reader.entered:
	case err := <-done:
		t.Fatalf("retry did not look up original metadata: %v", err)
	}
	_, busy := engine.RetagAudio(ctx, task, item.TrackID)
	close(reader.resume)
	err = <-done
	if !errors.Is(busy, syncer.ErrAudioBusy) {
		t.Fatalf("parallel retry: %v", busy)
	}
	if err != nil || !result.Changed || result.Warning != "" || result.Item.TagPending || !slices.Equal(result.Inspection.Missing, []string{"cover"}) {
		t.Fatalf("unexpected repair result: %+v, %v", result, err)
	}
	assertTaggedFLAC(t, data, result.Item, map[string][]string{
		taglib.Title: {"本地歌名"}, taglib.Artist: {"登记歌手"}, taglib.Album: {"平台专辑"}, taglib.Lyrics: {fixtureLRC},
	})
}
