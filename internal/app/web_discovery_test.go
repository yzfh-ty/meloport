package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"meloport/internal/model"
)

func (s *webTestSource) PlaylistCatalog(ctx context.Context) (model.PlaylistCatalog, error) {
	s.catalogReads.Add(1)
	if err := ctx.Err(); err != nil {
		return model.PlaylistCatalog{}, err
	}
	if s.fail.Load() {
		return model.PlaylistCatalog{}, errors.New("native fixture unavailable")
	}
	return model.PlaylistCatalog{Categories: []model.PlaylistCategory{{ID: "0", Name: "语种", Tags: []model.PlaylistTag{{ID: "华语", Name: "华语"}}}}, Sorts: []model.PlaylistSort{{ID: "hot", Name: "最热"}}}, nil
}

func (s *webTestSource) Playlists(ctx context.Context, category, order string, page int) (model.PlaylistPage, error) {
	s.playlistPages.Add(1)
	if err := ctx.Err(); err != nil {
		return model.PlaylistPage{}, err
	}
	if s.fail.Load() {
		return model.PlaylistPage{}, errors.New("native fixture unavailable")
	}
	result := model.PlaylistPage{Page: page, Limit: 30, Total: 1}
	if !s.emptyDiscovery.Load() {
		result.Items = []model.PlaylistSummary{{ID: "123", Name: "Native discovery"}}
	}
	return result, nil
}

func TestWebPlaylistDiscoveryIsNativeScopedAndCached(t *testing.T) {
	f := newWebFixture(t)
	for _, source := range []string{"netease", "qq", "kugou"} {
		for range 2 {
			webStatus(t, webRequest(t, f, "GET", "/api/playlists/catalog?platform="+source, nil), 200)
		}
		if f.sources[source].catalogReads.Load() != 1 {
			t.Fatal("categories were not cached per platform")
		}
		w := webRequest(t, f, "GET", "/api/playlists?platform="+source+"&category="+url.QueryEscape("华语")+"&page=2", nil)
		webStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), "Native discovery") || !strings.Contains(w.Body.String(), `"page":2`) {
			t.Fatal("lost native playlist directory")
		}
		webStatus(t, webRequest(t, f, "GET", "/api/playlists/catalog?platform="+source+"&refresh=1", nil), 200)
		if f.sources[source].catalogReads.Load() != 2 {
			t.Fatal("refresh did not bypass the category cache")
		}
	}
	f.sources["qq"].fail.Store(true)
	webStatus(t, webRequest(t, f, "GET", "/api/playlists?platform=qq", nil), 502)
	webStatus(t, webRequest(t, f, "GET", "/api/playlists/catalog?platform=qq&refresh=1", nil), 502)
	if f.gateway.calls.Load() != 0 {
		t.Fatal("discovery used a metered audio or search request")
	}
}

func TestWebDiscoveryValidatesQueriesAndProtectsMetadataReads(t *testing.T) {
	f := newWebFixture(t)
	for _, path := range []string{"/api/playlists?platform=other", "/api/playlists?platform=netease&page=0", "/api/playlists?platform=netease&page=101", "/api/playlists?platform=netease&page=NaN", "/api/playlists?platform=netease&category=not-a-native-tag", "/api/playlists?platform=netease&sort=unsupported"} {
		webStatus(t, webRequest(t, f, "GET", path, nil), 400)
	}
	if f.sources["netease"].playlistPages.Load() != 0 {
		t.Fatal("invalid query reached the platform playlist reader")
	}
	for _, path := range []string{"/api/playlists", "/api/playlists/catalog", "/api/playlists/netease/123"} {
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		webStatus(t, w, 403)
	}
	f.sources["netease"].emptyDiscovery.Store(true)
	w := webRequest(t, f, "GET", "/api/playlists?platform=netease", nil)
	webStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("valid empty native directory was not serialized as an array")
	}
	identity := "https://m3ws.kugou.com/share/index.php?global_collection_id=collection_3_42_123_0"
	w = webRequest(t, f, "GET", "/api/playlists/kugou/"+url.PathEscape(identity), nil)
	webStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), identity) || !strings.Contains(w.Body.String(), `"fetched_via":"native"`) {
		t.Fatal("encoded native playlist identity was changed by the detail route")
	}
	if f.gateway.calls.Load() != 0 {
		t.Fatal("discovery or preview reached the metered gateway")
	}
}

func TestWebPlaylistCategoryCacheConcurrentAccess(t *testing.T) {
	f := newWebFixture(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			webStatus(t, webRequest(t, f, "GET", "/api/playlists/catalog?platform=netease", nil), 200)
		}()
	}
	wg.Wait()
}
