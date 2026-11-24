package kugou

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

const testCollectionID = "collection_3_42_123_0"

func collectionSong(index int) map[string]any {
	return map[string]any{"hash": hashFor(index), "remark": fmt.Sprintf("Song %d", index), "name": fmt.Sprintf("Singer - Song %d", index), "singerinfo": []any{map[string]any{"name": "Singer"}}, "albuminfo": map[string]any{"name": "Album"}, "timelen": 190000, "cover": "http://imge.kugou.com/{size}/cover.jpg", "publish_date": "2025-03-04"}
}

func collectionData(offset, total int) map[string]any {
	rows := make([]any, 0, globalPageSize)
	for i := offset; i < min(offset+globalPageSize, total); i++ {
		rows = append(rows, collectionSong(i))
	}
	return map[string]any{"begin_idx": offset, "pagesize": globalPageSize, "count": total, "list_info": map[string]any{"global_collection_id": testCollectionID, "code": 1, "count": total, "name": "Public collection", "list_ver": 7}, "songs": rows}
}

func TestCurrentCollectionReadsEveryPageAndReusesNativeDetails(t *testing.T) {
	var calls atomic.Int32
	var verificationCalls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/get_list_info" && r.Method == http.MethodPost {
			verificationCalls.Add(1)
			if r.Header.Get("X-Router") != "pubsongs.kugou.com" {
				t.Error("missing native collection verification router")
			}
			jsonReply(t, w, map[string]any{"error_code": 0, "data": []any{collectionData(0, 205)["list_info"]}})
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/pubsongs/v2/get_other_list_file_nofilt" {
			t.Errorf("unexpected or unnecessary metadata request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		query := r.URL.Query()
		actual := query.Get("signature")
		query.Del("signature")
		payload, _ := io.ReadAll(r.Body)
		if len(payload) != 0 || actual != signature(query.Encode(), androidSigningMaterial, "") || query.Get("global_collection_id") != testCollectionID || query.Get("pagesize") != "100" || query.Get("appid") != "1005" {
			t.Error("lost native collection identity, signature or fixed page size")
		}
		offset := int(calls.Add(1)-1) * globalPageSize
		if query.Get("begin_idx") != fmt.Sprint(offset) {
			t.Error("incorrect page offset")
		}
		data := collectionData(offset, 205)
		if offset > 0 {
			data["list_info"] = map[string]any{}
		}
		jsonReply(t, w, map[string]any{"error_code": 0, "data": data})
	})
	identity := "https://m3ws.kugou.com/share/index.php?global_collection_id=" + testCollectionID
	list, err := client.Playlist(context.Background(), identity)
	if err != nil || calls.Load() != 3 || verificationCalls.Load() != 1 || list.ID != identity || list.ReportedCount != 205 || len(list.Tracks) != 205 {
		t.Fatalf("incomplete current collection: %+v, %v", list, err)
	}
	track := list.Tracks[100]
	if track.ID != hashFor(100) || track.Hash != hashFor(100) || track.Name != "Song 100" || track.Album != "Album" || len(track.Artists) != 1 || track.Duration != 190 || track.CoverURL != "https://imge.kugou.com/800/cover.jpg" || track.Year != "2025" {
		t.Fatalf("native collection song was substituted or lost metadata: %+v", track)
	}
}

func TestCurrentCollectionRejectsIncompleteOrChangedMetadata(t *testing.T) {
	for _, reason := range []string{"identity", "rejected", "count", "version", "cursor", "empty-page", "repeated-page", "invalid-hash", "changed-after-pagination"} {
		t.Run(reason, func(t *testing.T) {
			var calls atomic.Int32
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v3/get_list_info" {
					info := collectionData(0, 205)["list_info"].(map[string]any)
					if reason == "changed-after-pagination" {
						info["list_ver"] = 8
					}
					jsonReply(t, w, map[string]any{"error_code": 0, "data": []any{info}})
					return
				}
				if r.URL.Path != "/pubsongs/v2/get_other_list_file_nofilt" {
					t.Error("unexpected fallback request")
					w.WriteHeader(500)
					return
				}
				offset := int(calls.Add(1)-1) * globalPageSize
				data := collectionData(offset, 205)
				info := data["list_info"].(map[string]any)
				switch reason {
				case "identity":
					info["global_collection_id"] = "collection_3_42_999_0"
				case "rejected":
					info["code"] = 403
				case "count":
					info["count"] = 206
				case "version":
					if offset > 0 {
						info["list_ver"] = 8
					}
				case "cursor":
					if offset > 0 {
						data["begin_idx"] = 0
					}
				case "empty-page":
					if offset > 0 {
						data["songs"] = []any{}
					}
				case "repeated-page":
					if offset > 0 {
						data["songs"] = collectionData(0, 205)["songs"]
					}
				case "invalid-hash":
					data["songs"].([]any)[0].(map[string]any)["hash"] = "ambiguous"
				}
				jsonReply(t, w, map[string]any{"error_code": 0, "data": data})
			})
			list, err := client.Playlist(context.Background(), "https://m3ws.kugou.com/share/index.php?global_collection_id="+testCollectionID)
			if err == nil || len(list.Tracks) != 0 {
				t.Fatal("published a partial, rejected or changed collection")
			}
		})
	}
}

func TestCurrentCollectionEmptyIsSuccessful(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(t, w, map[string]any{"error_code": 0, "data": collectionData(0, 0)})
	})
	list, err := client.Playlist(context.Background(), "https://m3ws.kugou.com/share/index.php?global_collection_id="+testCollectionID)
	if err != nil || list.Tracks == nil || len(list.Tracks) != 0 || list.ReportedCount != 0 {
		t.Fatalf("empty native collection: %+v %v", list, err)
	}
}
