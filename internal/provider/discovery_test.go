package provider

import (
	"context"
	"errors"
	"testing"

	"meloport/internal/model"
)

type discoveryStubSource struct {
	stubSource
	calls int
	err   error
}

func (s *discoveryStubSource) PlaylistCatalog(context.Context) (model.PlaylistCatalog, error) {
	s.calls++
	return model.PlaylistCatalog{Categories: []model.PlaylistCategory{}, Sorts: []model.PlaylistSort{{ID: "hot", Name: "最热"}}}, s.err
}

func (s *discoveryStubSource) Playlists(context.Context, string, string, int) (model.PlaylistPage, error) {
	s.calls++
	return model.PlaylistPage{Items: []model.PlaylistSummary{}, Total: 0}, s.err
}

func TestDiscoveryNeverUsesMeteredGatewayOnEmptyFailureOrCancellation(t *testing.T) {
	source := &discoveryStubSource{}
	gateway := &stubGateway{}
	manager := New(map[string]Source{"netease": source, "extension": stubSource{}}, gateway)
	if _, err := manager.PlaylistCatalog(context.Background(), "netease"); err != nil {
		t.Fatal(err)
	}
	if page, err := manager.Playlists(context.Background(), "netease", "", "hot", 1); err != nil || page.Items == nil {
		t.Fatalf("native empty result failed: %+v %v", page, err)
	}
	source.err = errors.New("native unavailable")
	if _, err := manager.Playlists(context.Background(), "netease", "", "hot", 1); !errors.Is(err, source.err) {
		t.Fatal("native failure was replaced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.PlaylistCatalog(ctx, "netease"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was ignored")
	}
	if _, err := manager.Playlists(ctx, "netease", "", "hot", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was ignored")
	}
	if source.calls != 3 {
		t.Fatal("cancelled request reached the native reader")
	}
	if _, err := manager.PlaylistCatalog(context.Background(), "extension"); err == nil {
		t.Fatal("registered a placeholder directory for an extension")
	}
	if gateway.playlistCalls+gateway.searchCalls+gateway.resolveCalls != 0 {
		t.Fatal("discovery reached the metered gateway")
	}
}
