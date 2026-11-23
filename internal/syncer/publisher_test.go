package syncer_test

import (
	"context"
	"errors"
	"testing"

	"meloport/internal/model"
	"meloport/internal/state"
)

type playlistPublicationFixture struct {
	calls int
	err   error
}

func (p *playlistPublicationFixture) PublishPlaylist(_ context.Context, _ model.Task, _ model.Playlist, downloads []state.Download) error {
	p.calls++
	if len(downloads) != 2 {
		return errors.New("publisher did not receive the existing audio records")
	}
	return p.err
}

func TestPlaylistPublicationRunsOnSkippedSyncWithoutAnotherAudioDownload(t *testing.T) {
	provider := &fakeProvider{playlist: playlist("1", "2", "2")}
	engine, _, downloader, task := setup(t, provider)
	publisher := &playlistPublicationFixture{}
	engine.UsePlaylistPublisher(publisher)
	first, err := engine.Run(t.Context(), task)
	if err != nil || first.Downloaded != 2 || publisher.calls != 1 {
		t.Fatalf("initial publication: %+v, %v", first, err)
	}
	second, err := engine.Run(t.Context(), task)
	if err != nil || second.Downloaded != 0 || second.Skipped != 2 || publisher.calls != 2 || downloader.calls != 2 || len(provider.resolved) != 2 {
		t.Fatalf("playlist update caused another audio resolution or download: %+v, %v", second, err)
	}
	publisher.err = errors.New("songs are not indexed yet")
	third, err := engine.Run(t.Context(), task)
	if err != nil || third.Status != "success" || third.PlaylistWarning == "" || downloader.calls != 2 || len(provider.resolved) != 2 {
		t.Fatalf("waiting for Navidrome changed the audio sync outcome: %+v, %v", third, err)
	}
}
