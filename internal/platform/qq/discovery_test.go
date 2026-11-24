package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestQQPlaylistDiscoveryUsesActiveNativeProtocols(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/cgi-bin/musicu.fcg" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected playlist discovery request")
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		sub := request["playlist"].(map[string]any)
		params := sub["param"].(map[string]any)
		var data map[string]any
		switch sub["method"] {
		case "get_all_categories":
			if sub["module"] != "playlist.PlaylistAllCategoriesServer" {
				t.Error("wrong category protocol")
			}
			data = map[string]any{"v_group": []any{map[string]any{"group_id": 1, "group_name": "风格", "v_item": []any{map[string]any{"id": 34, "name": "R&#38;B"}}}}}
		case "get_category_content":
			if sub["module"] != "playlist.PlayListCategoryServer" || params["category_id"] != float64(34) || params["page"] != float64(1) || params["size"] != float64(30) {
				t.Error("wrong tagged playlist cursor")
			}
			data = map[string]any{"content": map[string]any{"total_cnt": 70, "v_item": []any{map[string]any{"basic": map[string]any{"tid": 12345, "title": "R&#38;B 歌单", "creator": map[string]any{"nick": "作者"}, "play_cnt": 500}}}}}
		case "GetRecommendWhole":
			if sub["module"] != "music.playlist.PlaylistSquare" || params["FeedReq"].(map[string]any)["From"] != float64(0) {
				t.Error("wrong recommendation protocol")
			}
			data = map[string]any{"FeedRsp": map[string]any{"HasMore": false, "FromLimit": 400, "List": nil}}
		default:
			t.Error("unsupported native protocol")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "playlist": map[string]any{"code": 0, "data": data}})
	})
	catalog, err := client.PlaylistCatalog(context.Background())
	if err != nil || catalog.Categories[0].Tags[0].Name != "R&B" || len(catalog.CategorySorts) != 1 || catalog.CategorySorts[0].ID != "default" {
		t.Fatalf("categories: %+v %v", catalog, err)
	}
	page, err := client.Playlists(context.Background(), "34", "default", 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "12345" || page.Items[0].Name != "R&B 歌单" || page.Items[0].TrackCount != nil || !page.HasMore || page.Total != 70 {
		t.Fatalf("directory: %+v %v", page, err)
	}
	empty, err := client.Playlists(context.Background(), "", "recommend", 1)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.Total != -1 || empty.HasMore {
		t.Fatalf("recommendation cursor was mistaken for a total: %+v %v", empty, err)
	}
	if _, err := client.Playlists(context.Background(), "34", "new", 1); err == nil {
		t.Fatal("accepted an ineffective sort for a tagged QQ category")
	}
}

func TestQQPlaylistDiscoveryPropagatesNativeRejection(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"playlist":{"code":403,"data":{}}}`)
	})
	if _, err := client.PlaylistCatalog(context.Background()); err == nil {
		t.Fatal("accepted a rejected category request")
	}
	if _, err := client.Playlists(context.Background(), "", "hot", 1); err == nil {
		t.Fatal("accepted a rejected playlist request")
	}
}
