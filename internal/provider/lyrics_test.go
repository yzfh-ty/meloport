package provider

import (
	"context"
	"errors"
	"testing"

	"meloport/internal/model"
)

type lyricStubSource struct {
	stubSource
	lyrics func(context.Context, model.Track) (string, error)
}

func (s lyricStubSource) Lyrics(ctx context.Context, track model.Track) (string, error) {
	return s.lyrics(ctx, track)
}

func TestLyricsNeverUsesMeteredGateway(t *testing.T) {
	sourceError := errors.New("native unavailable")
	gateway := &stubGateway{}
	manager := New(map[string]Source{
		"qq": stubSource{},
		"netease": lyricStubSource{lyrics: func(_ context.Context, track model.Track) (string, error) {
			if track.ID == "123" {
				return "[00:02.00]lyric", nil
			}
			return "", sourceError
		}},
	}, gateway)
	if got, err := manager.Lyrics(context.Background(), "netease", model.Track{ID: "123"}); err != nil || got != "[00:02.00]lyric" {
		t.Fatalf("native result = %q, %v", got, err)
	}
	if got, err := manager.Lyrics(context.Background(), "netease", model.Track{ID: "124"}); !errors.Is(err, sourceError) || got != "" {
		t.Fatalf("native failure = %q, %v", got, err)
	}
	if got, err := manager.Lyrics(context.Background(), "qq", model.Track{ID: "SongMID"}); err != nil || got != "" {
		t.Fatalf("unsupported lyric source = %q, %v", got, err)
	}
	if gateway.playlistCalls+gateway.searchCalls+gateway.resolveCalls != 0 {
		t.Fatal("lyrics contacted the metered gateway")
	}
}
