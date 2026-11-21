package syncer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"go.senan.xyz/taglib"

	"meloport/internal/download"
	"meloport/internal/model"
	"meloport/internal/provider"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

const fixtureLRC = "[00:01.25]测试第一行\n[00:02.50]测试第二行"

type lyricFixtureSource struct {
	nativeFixtureSource
	requested []string
	fail      bool
}

func (s *lyricFixtureSource) Lyrics(_ context.Context, track model.Track) (string, error) {
	s.requested = append(s.requested, track.ID)
	if s.fail && track.ID == "003Retry" {
		return "", errors.New("private upstream detail")
	}
	return fixtureLRC, nil
}

type fixtureAudioResolver func(context.Context, string, model.Track, string) (model.ResolvedTrack, error)

func (f fixtureAudioResolver) Resolve(ctx context.Context, platform string, track model.Track, quality string) (model.ResolvedTrack, error) {
	return f(ctx, platform, track, quality)
}

func TestSyncEmbedsLyricsAndBackfillsTrackedSongs(t *testing.T) {
	ctx := context.Background()
	payload, err := os.ReadFile(filepath.Join("..", "download", "testdata", "tone.flac"))
	if err != nil {
		t.Fatal(err)
	}
	var audioCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		audioCalls.Add(1)
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	list := playlist("003Legacy", "003New", "003Retry")
	native := &lyricFixtureSource{nativeFixtureSource: nativeFixtureSource{list: list}, fail: true}
	manager := provider.New(map[string]provider.Source{"qq": native}, nil, false, nil)
	dl, err := download.New(download.Options{HTTPClient: server.Client(), Lyrics: manager})
	if err != nil {
		t.Fatal(err)
	}
	var resolved []string
	resolver := fixtureAudioResolver(func(_ context.Context, source string, track model.Track, _ string) (model.ResolvedTrack, error) {
		if source != "qq" {
			t.Fatalf("audio resolution selected %q", source)
		}
		resolved = append(resolved, track.ID)
		return model.ResolvedTrack{URL: server.URL, Size: int64(len(payload))}, nil
	})

	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := model.Task{ID: "lyrics", Platform: "qq", PlaylistID: list.ID, Directory: filepath.Join(dir, "music")}
	if err := os.MkdirAll(task.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := list.Tracks[0]
	legacyPath := filepath.Join(task.Directory, "qq-"+legacy.ID+".flac")
	if err := os.WriteFile(legacyPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := taglib.WriteTags(legacyPath, map[string][]string{taglib.Title: {legacy.Name}, taglib.Artist: legacy.Artists}, 0); err != nil {
		t.Fatal(err)
	}
	legacyAudio, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := sha256.Sum256(legacyAudio)
	// Keep the exact v1 fingerprint input. An arbitrary stale value would also
	// trigger retagging if the application forgot to invalidate real v1 records.
	oldFingerprint := sha256.Sum256([]byte("meloport-tags-v1\n" + `{"id":"","name":"Song 003Legacy","artists":["Artist"],"album":""}`))
	if err := store.SaveDownload(ctx, state.Download{
		TaskID: task.ID, Provider: task.Platform, TrackID: legacy.ID, Path: legacyPath,
		Size: int64(len(legacyAudio)), SHA256: hex.EncodeToString(oldDigest[:]), Metadata: legacy,
		TagFingerprint: hex.EncodeToString(oldFingerprint[:]),
	}); err != nil {
		t.Fatal(err)
	}
	assertTrack := func(id, title string, lrc []string) {
		t.Helper()
		item, found, err := store.GetDownload(ctx, task.ID, task.Platform, id)
		if err != nil || !found || item.TagPending {
			t.Fatalf("track %s not recorded as complete: %+v %v", id, item, err)
		}
		assertTaggedFLAC(t, payload, item, map[string][]string{taglib.Title: {title}, "LYRICS": lrc})
	}

	engine := syncer.NewWithSources(manager, resolver, dl, store)
	first, err := engine.Run(ctx, task)
	if err != nil || first.Status != "success" || first.Downloaded != 2 || first.Skipped != 1 || len(first.TagWarnings) != 1 {
		t.Fatalf("sync with backfill and one lyric outage: %+v %v", first, err)
	}
	if !strings.Contains(first.TagWarnings[0], "003Retry") || strings.Contains(first.TagWarnings[0], "private upstream detail") {
		t.Fatalf("lyric error was not reported safely: %v", first.TagWarnings)
	}
	assertTrack(legacy.ID, legacy.Name, []string{fixtureLRC})
	assertTrack("003New", "Song 003New", []string{fixtureLRC})
	assertTrack("003Retry", "Song 003Retry", nil)
	if !slices.Equal(native.requested, []string{"003Legacy", "003New", "003Retry"}) {
		t.Fatalf("unexpected lyric lookups: %v", native.requested)
	}

	native.requested = nil
	second, err := engine.Run(ctx, task)
	if err != nil || second.Downloaded != 0 || second.Skipped != 3 || len(native.requested) != 0 {
		t.Fatalf("ordinary sync repeated completed tagging: %+v %v lookups=%v", second, err, native.requested)
	}

	native.fail = false
	retagged, err := engine.Retag(ctx, task)
	if err != nil || retagged.Tagged != 1 || retagged.Skipped != 2 || !slices.Equal(native.requested, []string{"003Retry"}) {
		t.Fatalf("explicit retag did not retry only the missing lyric: %+v %v lookups=%v", retagged, err, native.requested)
	}
	assertTrack("003Retry", "Song 003Retry", []string{fixtureLRC})
	if !slices.Equal(resolved, []string{"003New", "003Retry"}) || audioCalls.Load() != 2 {
		t.Fatalf("existing audio was resolved or downloaded: resolved=%v requests=%d", resolved, audioCalls.Load())
	}
}
