package kugou

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestKugouDiscoveryUsesCurrentSignedCategoryAndPlaylistProtocols(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		query := r.URL.Query()
		actual := query.Get("signature")
		query.Del("signature")
		if r.Method != http.MethodPost || actual != signature(query.Encode(), androidSigningMaterial, string(payload)) || query.Get("appid") != "1005" || query.Get("clientver") != "20489" {
			t.Error("invalid native Android discovery signature")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credentials must not leak into clear-text fixture requests")
		}
		var body map[string]any
		_ = json.Unmarshal(payload, &body)
		switch r.URL.Path {
		case "/pubsongs/v1/get_tags_by_type":
			if body["tag_type"] != "collection" || body["source"] != float64(3) {
				t.Error("wrong category protocol")
			}
			jsonReply(t, w, map[string]any{"status": 1, "error_code": 0, "data": []any{map[string]any{"tag_id": "5", "tag_name": "场景", "son": []any{map[string]any{"tag_id": "587", "tag_name": "学习"}}}}})
		case "/v2/special_recommend":
			params := body["special_recommend"].(map[string]any)
			if params["categoryid"] != float64(587) || params["withsong"] != float64(0) || params["withrecommend"] != float64(0) || body["page"] != float64(2) || body["pagesize"] != float64(30) || r.Header.Get("X-Router") != "specialrec.service.kugou.com" {
				t.Error("lost native category or pagination")
			}
			jsonReply(t, w, map[string]any{"status": 1, "error_code": 0, "data": map[string]any{"has_next": 1, "special_list": []any{map[string]any{"specialid": 123, "global_collection_id": "collection_3_42_123_0", "specialname": "学习", "nickname": "作者", "imgurl": "http://imge.kugou.com/{size}/cover.jpg", "songcount": 0, "play_count": 10, "tags": []any{map[string]any{"tag_name": "学习"}}}}}})
		default:
			w.WriteHeader(404)
		}
	})
	catalog, err := client.PlaylistCatalog(context.Background())
	if err != nil || len(catalog.Categories) != 1 || catalog.Categories[0].Tags[0].ID != "587" {
		t.Fatalf("categories: %+v %v", catalog, err)
	}
	page, err := client.Playlists(context.Background(), "587", "recommend", 2)
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.Total != -1 || page.Items[0].TrackCount != nil || page.Items[0].CoverURL != "https://imge.kugou.com/300/cover.jpg" {
		t.Fatalf("directory: %+v %v", page, err)
	}
	want := "https://m3ws.kugou.com/share/index.php?global_collection_id=collection_3_42_123_0"
	if got, err := NormalizePlaylist(page.Items[0].ID); err != nil || got != want {
		t.Fatalf("discovery confused a playlist with a numeric share code: %q, %v", got, err)
	}
}

func TestKugouDiscoveryAcceptsEmptyButRejectsUnsupportedIdentities(t *testing.T) {
	for _, test := range []struct {
		name  string
		rows  []any
		valid bool
	}{
		{"empty", []any{}, true},
		{"special-id", []any{map[string]any{"specialid": 123, "specialname": "List"}}, true},
		{"ambiguous", []any{map[string]any{"specialid": 0, "global_collection_id": "../private", "specialname": "List"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				jsonReply(t, w, map[string]any{"status": 1, "error_code": 0, "data": map[string]any{"has_next": 0, "special_list": test.rows}})
			})
			page, err := client.Playlists(context.Background(), "", "recommend", 1)
			if (err == nil) != test.valid || (test.valid && page.Items == nil) {
				t.Fatalf("directory: %+v %v", page, err)
			}
			if test.name == "special-id" && page.Items[0].ID != "id_123" {
				t.Fatal("numeric special ID was exposed as a share code")
			}
		})
	}
}
