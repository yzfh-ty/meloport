package netease

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativePlaylistDiscoveryPreservesCategoriesAndPageCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.URL.Query().Has("apikey") {
			t.Error("discovery must use native read-only metadata without gateway credentials")
		}
		switch r.URL.Path {
		case "/api/playlist/catalogue":
			fmt.Fprint(w, `{"code":200,"categories":{"0":"语种","1":"风格"},"sub":[{"name":"华语","category":0,"hot":true},{"name":"摇滚","category":1,"hot":false}]}`)
		case "/api/playlist/list":
			if r.URL.Query().Get("cat") != "华语" || r.URL.Query().Get("order") != "new" || r.URL.Query().Get("offset") != "30" || r.URL.Query().Get("limit") != "30" {
				t.Error("lost category, sort or native pagination cursor")
			}
			fmt.Fprint(w, `{"code":200,"total":88,"more":true,"playlists":[{"id":9007199254740993,"name":"学习","creator":{"nickname":"作者"},"coverImgUrl":"http://p2.music.126.net/cover.jpg","trackCount":20,"playCount":12345,"tags":["华语"]},{"id":9007199254740993,"name":"学习"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := fixtureClient(server)
	catalog, err := client.PlaylistCatalog(context.Background())
	if err != nil || len(catalog.Categories) != 2 || catalog.Categories[0].Tags[0].ID != "华语" || !catalog.Categories[0].Tags[0].Hot {
		t.Fatalf("unexpected native categories: %+v, %v", catalog, err)
	}
	page, err := client.Playlists(context.Background(), "华语", "new", 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "9007199254740993" || page.Page != 2 || !page.HasMore || page.Total != 88 || page.Items[0].Author != "作者" || *page.Items[0].TrackCount != 20 {
		t.Fatalf("unexpected native directory: %+v, %v", page, err)
	}
}

func TestPlaylistDiscoveryEmptySuccessAndNativeFailures(t *testing.T) {
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"empty", `{"code":200,"total":0,"more":false,"playlists":[]}`, true},
		{"native-failure", `{"code":403,"total":0,"more":false,"playlists":[]}`, false},
		{"missing-list", `{"code":200,"total":0,"more":false}`, false},
		{"invalid-id", `{"code":200,"total":1,"more":false,"playlists":[{"id":"ambiguous","name":"List"}]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, test.body) }))
			defer server.Close()
			page, err := fixtureClient(server).Playlists(context.Background(), "", "hot", 1)
			if (err == nil) != test.valid || (test.valid && (page.Items == nil || page.HasMore)) {
				t.Fatalf("unexpected result: %+v, %v", page, err)
			}
		})
	}
}
