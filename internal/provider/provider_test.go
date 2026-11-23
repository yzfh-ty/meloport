package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"meloport/internal/model"
)

type stubSource struct {
	playlist func(context.Context, string) (model.Playlist, error)
	search   func(context.Context, string, int, int) ([]model.Track, error)
}

type rankingStubSource struct {
	stubSource
	rankings []model.Ranking
	ranking  func(context.Context, string) (model.Playlist, error)
}

func (s rankingStubSource) Rankings(context.Context) ([]model.Ranking, error) {
	return s.rankings, nil
}

func (s rankingStubSource) Ranking(ctx context.Context, id string) (model.Playlist, error) {
	if s.ranking != nil {
		return s.ranking(ctx, id)
	}
	return model.Playlist{}, errors.New("unexpected native ranking")
}

func (s stubSource) Playlist(ctx context.Context, id string) (model.Playlist, error) {
	if s.playlist != nil {
		return s.playlist(ctx, id)
	}
	return model.Playlist{}, errors.New("unexpected native playlist")
}

func (s stubSource) Search(ctx context.Context, q string, limit, offset int) ([]model.Track, error) {
	if s.search != nil {
		return s.search(ctx, q, limit, offset)
	}
	return nil, errors.New("unexpected native search")
}

type stubGateway struct {
	playlistCalls, searchCalls, resolveCalls int
	playlist                                 func(context.Context, string) (model.Playlist, error)
	search                                   func(context.Context, string, string, int, int) ([]model.Track, error)
	resolve                                  func(context.Context, string, string, string) (model.ResolvedTrack, error)
}

func (g *stubGateway) Playlist(ctx context.Context, id string) (model.Playlist, error) {
	g.playlistCalls++
	if g.playlist != nil {
		return g.playlist(ctx, id)
	}
	return model.Playlist{}, errors.New("unexpected metered playlist")
}

func (g *stubGateway) Search(ctx context.Context, source, q string, limit, offset int) ([]model.Track, error) {
	g.searchCalls++
	if g.search != nil {
		return g.search(ctx, source, q, limit, offset)
	}
	return nil, errors.New("unexpected metered search")
}

func (g *stubGateway) ResolvePlatform(ctx context.Context, source, id, quality string) (model.ResolvedTrack, error) {
	g.resolveCalls++
	if g.resolve != nil {
		return g.resolve(ctx, source, id, quality)
	}
	return model.ResolvedTrack{}, errors.New("unexpected metered resolve")
}

func TestNativeMetadataSuccessNeverTouchesMeteredGateway(t *testing.T) {
	for _, source := range []string{"netease", "qq", "kugou"} {
		for _, empty := range []bool{false, true} {
			name := source
			if empty {
				name += "/empty"
			}
			t.Run(name, func(t *testing.T) {
				gateway := &stubGateway{}
				tracks := []model.Track{{ID: "123", Name: "Native"}}
				if empty {
					tracks = []model.Track{}
				}
				manager := New(map[string]Source{source: stubSource{
					playlist: func(ctx context.Context, id string) (model.Playlist, error) {
						if id != "playlist123" {
							t.Error("changed native playlist ID")
						}
						return model.Playlist{ID: id, Tracks: tracks, ReportedCount: len(tracks)}, nil
					},
					search: func(ctx context.Context, q string, limit, offset int) ([]model.Track, error) {
						if q != "query" || limit != 10 || offset != 20 {
							t.Error("changed native search")
						}
						return tracks, nil
					},
				}}, gateway)
				playlist, err := manager.Playlist(context.Background(), source, "playlist123")
				if err != nil || playlist.FetchedVia != "native" || !reflect.DeepEqual(playlist.Tracks, tracks) {
					t.Fatalf("native playlist failed: %+v %v", playlist, err)
				}
				results, err := manager.Search(context.Background(), source, "query", 10, 20)
				if err != nil || !reflect.DeepEqual(results, tracks) {
					t.Fatalf("native search failed: %+v %v", results, err)
				}
				if gateway.playlistCalls+gateway.searchCalls+gateway.resolveCalls != 0 {
					t.Fatal("successful native metadata consumed metered gateway")
				}
			})
		}
	}
}

func TestNativeMetadataFailureNeverConsumesGateway(t *testing.T) {
	nativeFailure := errors.New("native connection refused")
	for _, source := range []string{"netease", "qq", "kugou"} {
		t.Run(source, func(t *testing.T) {
			gateway := &stubGateway{}
			manager := New(map[string]Source{source: stubSource{
				playlist: func(context.Context, string) (model.Playlist, error) { return model.Playlist{}, nativeFailure },
				search:   func(context.Context, string, int, int) ([]model.Track, error) { return nil, nativeFailure },
			}}, gateway)
			if _, err := manager.Playlist(context.Background(), source, "123"); !errors.Is(err, nativeFailure) {
				t.Fatalf("native playlist cause lost: %v", err)
			}
			if _, err := manager.Search(context.Background(), source, "query", 10, 2); !errors.Is(err, nativeFailure) {
				t.Fatalf("native search cause lost: %v", err)
			}
			if gateway.playlistCalls+gateway.searchCalls+gateway.resolveCalls != 0 {
				t.Fatal("native metadata failure consumed the gateway")
			}
		})
	}
}
func TestCanceledMetadataNeverFallsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gateway := &stubGateway{}
	manager := New(map[string]Source{"netease": stubSource{playlist: func(context.Context, string) (model.Playlist, error) {
		return model.Playlist{}, errors.New("native stopped")
	}, search: func(context.Context, string, int, int) ([]model.Track, error) { return nil, context.Canceled }}}, gateway)
	if _, err := manager.Playlist(ctx, "netease", "123"); !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong cancel error: %v", err)
	}
	if _, err := manager.Search(ctx, "netease", "query", 10, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong cancel error: %v", err)
	}
	if gateway.playlistCalls+gateway.searchCalls != 0 {
		t.Fatal("cancellation triggered metered fallback")
	}
}

func TestUnknownSourceAndAbsentGateway(t *testing.T) {
	gateway := &stubGateway{}
	manager := New(map[string]Source{}, gateway)
	if _, err := manager.Playlist(context.Background(), "unknown", "123"); err == nil {
		t.Fatal("accepted unknown playlist source")
	}
	if _, err := manager.Search(context.Background(), "unknown", "q", 20, 0); err == nil {
		t.Fatal("accepted unknown search source")
	}
	if _, err := manager.Resolve(context.Background(), "unknown", model.Track{ID: "1"}, ""); err == nil {
		t.Fatal("accepted unknown audio source")
	}
	if gateway.playlistCalls+gateway.searchCalls+gateway.resolveCalls != 0 {
		t.Fatal("unknown source touched gateway")
	}
	manager = New(nil, nil)
	if _, err := manager.Resolve(context.Background(), "qq", model.Track{ID: "MID"}, ""); err == nil {
		t.Fatal("missing gateway accepted")
	}
}

func TestRankingCatalogAndSubscriptionStayNative(t *testing.T) {
	var nativeCalls int
	rankings := []model.Ranking{{ID: "26", Name: "QQ榜单", Category: "热榜", TrackCount: 100, UpdateFrequency: "每日"}}
	source := rankingStubSource{
		rankings: rankings,
		ranking: func(ctx context.Context, id string) (model.Playlist, error) {
			nativeCalls++
			if id != "26" {
				t.Errorf("ranking ID changed: %q", id)
			}
			return model.Playlist{ID: id, Name: "QQ榜单", Tracks: []model.Track{}, ReportedCount: 0}, nil
		},
	}
	gateway := &stubGateway{}
	manager := New(map[string]Source{"qq": source}, gateway)
	got, err := manager.Rankings(context.Background(), "qq")
	if err != nil || !reflect.DeepEqual(got, rankings) {
		t.Fatalf("ranking directory failed: %+v %v", got, err)
	}
	playlist, err := manager.PlaylistFor(context.Background(), "qq", "ranking", "26")
	if err != nil || playlist.FetchedVia != "native" || playlist.ID != "26" {
		t.Fatalf("native ranking read failed: %+v %v", playlist, err)
	}
	if nativeCalls != 1 || gateway.playlistCalls != 0 {
		t.Fatalf("ranking read used wrong source: native=%d gateway=%d", nativeCalls, gateway.playlistCalls)
	}
	if _, err := manager.PlaylistFor(context.Background(), "qq", "unsupported", "26"); err == nil {
		t.Fatal("accepted unsupported subscription source type")
	}
	if _, err := manager.Rankings(context.Background(), "unknown"); err == nil {
		t.Fatal("accepted unsupported ranking platform")
	}
}

func TestResolveUsesPlatformIdentityWithoutMetadataRequests(t *testing.T) {
	for _, source := range []string{"netease", "qq"} {
		t.Run(source, func(t *testing.T) {
			id, quality := "123", "lossless"
			if source == "qq" {
				id, quality = "003NativeMID", "flac"
			}
			gateway := &stubGateway{resolve: func(ctx context.Context, gotSource, gotID, gotQuality string) (model.ResolvedTrack, error) {
				if gotSource != source || gotID != id || gotQuality != quality {
					t.Error("wrong platform audio identity")
				}
				return model.ResolvedTrack{URL: "https://audio.example/song.flac"}, nil
			}}
			manager := New(nil, gateway)
			if _, err := manager.Resolve(context.Background(), source, model.Track{ID: id}, quality); err != nil {
				t.Fatal(err)
			}
			if gateway.resolveCalls != 1 || gateway.searchCalls != 0 || gateway.playlistCalls != 0 {
				t.Fatal("direct ID resolution performed metadata requests")
			}
		})
	}
}

func TestKugouSearchOccursOnlyDuringResolution(t *testing.T) {
	track := model.Track{ID: "native-audio-id", Name: "Song", Artists: []string{"Singer"}, Album: "Album", Duration: 200, Hash: "AABB"}
	gateway := &stubGateway{
		search: func(ctx context.Context, source, q string, limit, offset int) ([]model.Track, error) {
			if source != "kugou" || q != "Song Singer" || limit != 50 || offset != 0 {
				t.Error("incorrect ID confirmation search")
			}
			return []model.Track{{ID: "chksz-own-id", Hash: "aabb"}}, nil
		},
		resolve: func(ctx context.Context, source, id, quality string) (model.ResolvedTrack, error) {
			if source != "kugou" || id != "chksz-own-id" || quality != "flac" {
				t.Error("native Kugou ID was guessed")
			}
			return model.ResolvedTrack{URL: "https://audio.example/confirmed.flac"}, nil
		},
	}
	manager := New(map[string]Source{"kugou": stubSource{playlist: func(context.Context, string) (model.Playlist, error) {
		return model.Playlist{Tracks: []model.Track{track}}, nil
	}, search: func(context.Context, string, int, int) ([]model.Track, error) { return []model.Track{track}, nil }}}, gateway)
	if _, err := manager.Playlist(context.Background(), "kugou", "playlist"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Search(context.Background(), "kugou", "q", 20, 0); err != nil {
		t.Fatal(err)
	}
	if gateway.searchCalls+gateway.resolveCalls != 0 {
		t.Fatal("Kugou metadata consumed audio gateway")
	}
	if _, err := manager.Resolve(context.Background(), "kugou", track, "flac"); err != nil {
		t.Fatal(err)
	}
	if gateway.searchCalls != 1 || gateway.resolveCalls != 1 {
		t.Fatal("unexpected Kugou resolver call count")
	}
	track.Name = ""
	if _, err := manager.Resolve(context.Background(), "kugou", track, "flac"); err == nil {
		t.Fatal("unconfirmed track with no metadata accepted")
	}
}

func TestMatchKugouConfirmsIdentityAndRejectsAmbiguity(t *testing.T) {
	base := model.Track{ID: "native", Name: " Song ", Artists: []string{"B", "A"}, Album: "Record", Duration: 200, Hash: "AABB"}
	matching := model.Track{ID: "confirmed", Name: "song", Artists: []string{"a / b"}, Album: "record", Duration: 201}
	for _, tc := range []struct {
		name       string
		track      model.Track
		candidates []model.Track
		want       string
		wantErr    bool
	}{
		{"unique metadata", base, []model.Track{matching}, "confirmed", false},
		{"duplicate same ID", base, []model.Track{matching, matching}, "confirmed", false},
		{"exact hash", base, []model.Track{{ID: "hash-confirmed", Hash: "aabb"}}, "hash-confirmed", false},
		{"hash as API ID", base, []model.Track{{ID: "aabb"}}, "aabb", false},
		{"two exact hash IDs", base, []model.Track{{ID: "one", Hash: "aabb"}, {ID: "two", Hash: "AABB"}}, "", true},
		{"two metadata versions", base, []model.Track{matching, {ID: "other", Name: "Song", Artists: []string{"A", "B"}, Album: "Record", Duration: 200}}, "", true},
		{"wrong title", base, []model.Track{{ID: "wrong", Name: "Song Live", Artists: []string{"A", "B"}, Album: "Record", Duration: 200}}, "", true},
		{"wrong artist", base, []model.Track{{ID: "wrong", Name: "Song", Artists: []string{"Someone"}, Album: "Record", Duration: 200}}, "", true},
		{"wrong album", base, []model.Track{{ID: "wrong", Name: "Song", Artists: []string{"A", "B"}, Album: "Other", Duration: 200}}, "", true},
		{"missing candidate album", base, []model.Track{{ID: "wrong", Name: "Song", Artists: []string{"A", "B"}, Duration: 200}}, "", true},
		{"wrong duration", base, []model.Track{{ID: "wrong", Name: "Song", Artists: []string{"A", "B"}, Album: "Record", Duration: 210}}, "", true},
		{"missing candidate duration", base, []model.Track{{ID: "wrong", Name: "Song", Artists: []string{"A", "B"}, Album: "Record"}}, "", true},
		{"conflicting hash", base, []model.Track{{ID: "wrong", Name: "Song", Artists: []string{"A", "B"}, Album: "Record", Duration: 200, Hash: "BBCC"}}, "", true},
		{"missing original album", model.Track{Name: "Song", Artists: []string{"A", "B"}, Duration: 200}, []model.Track{matching}, "", true},
		{"missing original duration", model.Track{Name: "Song", Artists: []string{"A", "B"}, Album: "Record"}, []model.Track{matching}, "", true},
		{"no match", base, nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchKugou(tc.track, tc.candidates)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("matched %q,err=%v; want %q err=%t", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
