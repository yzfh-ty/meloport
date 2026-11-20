package kugou

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meloport/internal/platform"
)

func hashFor(index int) string { return fmt.Sprintf("%032x", index+1) }

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Options{BaseURL: server.URL, HTTP: platform.NewHTTP(server.Client(), time.Second, 0)})
}

func jsonReply(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func detailRows(t *testing.T, r *http.Request) []any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
		return nil
	}
	if r.Header.Get("X-Router") != "kmr.service.kugou.com" || !strings.HasPrefix(r.Header.Get("User-Agent"), "Android") {
		t.Error("missing audio metadata routing headers")
	}
	if strings.Contains(r.URL.RawQuery, "apikey") {
		t.Error("key sent to native platform")
	}
	var rows []any
	for _, raw := range body["data"].([]any) {
		hash := raw.(map[string]any)["hash"].(string)
		rows = append(rows, []any{map[string]any{"songname": "Song &amp; " + hash, "author_name": "Singer A、Singer B", "album_info": map[string]any{"album_name": "Album"}, "audio_info": map[string]any{"hash": hash, "audio_id": "42", "timelength": "123400"}}})
	}
	return rows
}

func TestNormalizePlaylist(t *testing.T) {
	valid := map[string]string{
		"123": "123", " id_123 ": "id_123", "gcid_ABC_123": "gcid_ABC_123",
		"https://www.kugou.com/yy/special/single/123.html?foo=bar":                            "id_123",
		"https://www2.kugou.kugou.com/special/single/123.html":                                "id_123",
		"https://m3ws.kugou.com/share/index.php?global_collection_id=abc&chain=def&ignored=1": "https://m3ws.kugou.com/share/index.php?chain=def&global_collection_id=abc",
		"https://www.kugou.com/songlist/gcid_ABC/?cover=http%3A%2F%2Fexample.com":             "gcid_ABC",
		"https://m.kugou.com/share/foo?chain=abc":                                             "https://m3ws.kugou.com/share/index.php?chain=abc",
	}
	for input, want := range valid {
		t.Run(input, func(t *testing.T) {
			got, err := NormalizePlaylist(input)
			if err != nil || got != want {
				t.Fatalf("got %q, %v want %q", got, err, want)
			}
		})
	}
	for _, input := range []string{"", "0", "-1", "id_0", "abc", "https://evil.test/?chain=abc", "https://kugou.com.evil.test/?chain=abc", "https://user:{{PASSWORD_1obvc1qn}}@m.kugou.com/?chain=abc", "https://m.kugou.com:443/?chain=abc", "https://m.kugou.com/?chain=../secret", "https://t.kugou.com/short", "https://www.kugou.com/song/#hash=abc"} {
		t.Run(input, func(t *testing.T) {
			if _, err := NormalizePlaylist(input); err == nil {
				t.Fatal("accepted unsafe/unknown input")
			}
		})
	}
}

func TestSignatureCanonicalOrdering(t *testing.T) {
	const key = "test-signing-material"
	want := md5.Sum([]byte(key + "a=1b=2" + `{"x":1}` + key))
	if got := signature("b=2&a=1", key, `{"x":1}`); got != hex.EncodeToString(want[:]) {
		t.Fatalf("wrong canonical signature %s", got)
	}
}

func assertSigned(t *testing.T, r *http.Request, key string, body string) {
	t.Helper()
	query := r.URL.Query()
	received := query.Get("signature")
	query.Del("signature")
	if received == "" || received != signature(query.Encode(), key, body) {
		t.Error("invalid signature")
	}
}

func TestChainPaginationAndMetadata(t *testing.T) {
	var pageCalls, detailCalls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/zlist/list":
			pageCalls.Add(1)
			assertSigned(t, r, webSigningMaterial, "")
			if r.URL.Query().Get("pagesize") != "50" {
				t.Error("page size changed")
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			start := (page - 1) * 50
			var list []any
			for i := start; i < min(start+50, 51); i++ {
				list = append(list, map[string]any{"hash": strings.ToUpper(hashFor(i))})
			}
			jsonReply(t, w, chainBody(51, list))
		case "/v3/album_audio/audio":
			detailCalls.Add(1)
			jsonReply(t, w, map[string]any{"error_code": 0, "data": detailRows(t, r)})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	list, err := client.Playlist(context.Background(), "https://m3ws.kugou.com/share/index.php?global_collection_id=abc&chain=def")
	if err != nil {
		t.Fatal(err)
	}
	if pageCalls.Load() != 2 || detailCalls.Load() != 1 || list.ReportedCount != 51 || len(list.Tracks) != 51 || list.Name != "My playlist" {
		t.Fatalf("bad complete playlist: %+v", list)
	}
	first := list.Tracks[0]
	if first.ID != hashFor(0) || first.Hash != hashFor(0) || first.Name != "Song & "+hashFor(0) || first.Duration != 123 || len(first.Artists) != 2 || first.Album != "Album" {
		t.Fatalf("bad metadata: %+v", first)
	}
}

func chainBody(count int, list []any) map[string]any {
	return map[string]any{"errcode": 0, "data": map[string]any{"info": []any{map[string]any{"name": "My playlist", "count": count}}, "list": map[string]any{"info": list}}}
}

func TestGlobalPaginationKeepsFixedSize(t *testing.T) {
	var pages, details atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/album_audio/audio" {
			assertSigned(t, r, webSigningMaterial, "")
		}
		switch r.URL.Path {
		case "/api/v5/special/info_v2":
			jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"songcount": 101, "specialname": "Global"}})
		case "/api/v5/special/song_v2":
			pages.Add(1)
			if r.URL.Query().Get("pagesize") != "100" {
				t.Error("last page changed pagesize")
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			var values []any
			for i := (page - 1) * 100; i < min(page*100, 101); i++ {
				values = append(values, map[string]any{"hash": hashFor(i)})
			}
			jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"info": values}})
		case "/v3/album_audio/audio":
			details.Add(1)
			jsonReply(t, w, map[string]any{"error_code": 0, "data": detailRows(t, r)})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	list, err := client.Playlist(context.Background(), "https://m3ws.kugou.com/share/index.php?global_collection_id=abc")
	if err != nil {
		t.Fatal(err)
	}
	if pages.Load() != 2 || details.Load() != 2 || len(list.Tracks) != 101 {
		t.Fatal("pagination/detail batching incomplete")
	}
}

func TestEncodedGlobalDecodeAndEmptyList(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/v1/songlist/batch_decode" {
			var payload json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			assertSigned(t, r, androidSigningMaterial, string(payload))
			jsonReply(t, w, map[string]any{"error_code": 0, "data": map[string]any{"list": []any{map[string]any{"global_collection_id": "decoded"}}}})
			return
		}
		if r.URL.Path != "/api/v5/special/info_v2" || r.URL.Query().Get("global_specialid") != "decoded" {
			t.Error("wrong decoded ID route")
		}
		jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"songcount": 0, "specialname": "Empty"}})
	})
	list, err := client.Playlist(context.Background(), "gcid_encoded")
	if err != nil || list.Tracks == nil || len(list.Tracks) != 0 || calls.Load() != 2 {
		t.Fatalf("empty list: %+v %v", list, err)
	}
}

func TestSpecialHTMLAndShareCode(t *testing.T) {
	var commands, specials atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/command/":
			commands.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["data"] != "123" {
				t.Error("numeric share code confused with special ID")
			}
			jsonReply(t, w, map[string]any{"error_code": 0, "data": map[string]any{"info": map[string]any{"type": 2, "id": 456, "name": "Shared"}}})
		case "/yy/special/single/456.html", "/yueku/v9/special/single/456-5-9999.html":
			specials.Add(1)
			fmt.Fprintf(w, `<script>global = {name: "Example &amp; playlist", count: 2}; global.data = [{"hash":%q},{"hash":%q}];</script>`, hashFor(1), hashFor(1))
		case "/v3/album_audio/audio":
			jsonReply(t, w, map[string]any{"error_code": 0, "data": detailRows(t, r)})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	list, err := client.Playlist(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if list.ID != "123" || list.Name != "Example & playlist" || len(list.Tracks) != 2 || list.Tracks[0].ID != list.Tracks[1].ID || commands.Load() != 1 || specials.Load() != 1 {
		t.Fatalf("wrong share/special route: %+v", list)
	}
}

func TestRejectIncompleteRepeatedAndMalformed(t *testing.T) {
	cases := map[string]func(page int) map[string]any{
		"missing-info": func(int) map[string]any {
			return map[string]any{"errcode": 0, "data": map[string]any{"list": map[string]any{"info": []any{}}}}
		},
		"negative-total": func(int) map[string]any { return chainBody(-1, []any{}) },
		"excess-total":   func(int) map[string]any { return chainBody(5001, []any{}) },
		"missing-list": func(int) map[string]any {
			return map[string]any{"errcode": 0, "data": map[string]any{"info": []any{map[string]any{"count": 1}}}}
		},
		"truncated-page": func(int) map[string]any { return chainBody(2, []any{map[string]any{"hash": hashFor(1)}}) },
		"invalid-hash":   func(int) map[string]any { return chainBody(1, []any{map[string]any{"hash": "../no"}}) },
		"business-error": func(int) map[string]any {
			return map[string]any{"errcode": 402, "msg": "sensitive-upstream-diagnostic"}
		},
		"no-status": func(int) map[string]any { return map[string]any{"data": map[string]any{}} },
		"repeated-page": func(int) map[string]any {
			var list []any
			for i := 0; i < 50; i++ {
				list = append(list, map[string]any{"hash": hashFor(i)})
			}
			return chainBody(100, list)
		},
		"changed-total": func(page int) map[string]any {
			var list []any
			for i := 0; i < 50; i++ {
				list = append(list, map[string]any{"hash": hashFor(i + (page-1)*50)})
			}
			if page == 1 {
				return chainBody(100, list)
			}
			return chainBody(101, list)
		},
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/zlist/list" {
					t.Error("unexpected enrichment after malformed list")
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				jsonReply(t, w, response(page))
			})
			_, err := client.Playlist(context.Background(), "https://m3ws.kugou.com/?chain=abc&global_collection_id=def")
			if err == nil {
				t.Fatal("accepted malformed/incomplete playlist")
			}
			if strings.Contains(err.Error(), "sensitive-upstream-diagnostic") {
				t.Fatal("raw upstream message leaked")
			}
		})
	}
}

func TestMissingMetadataAndHTMLCount(t *testing.T) {
	for _, mode := range []string{"missing-details", "wrong-hash", "missing-html", "html-count", "empty-detail-row"} {
		t.Run(mode, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "special/single") {
					if mode == "missing-html" {
						fmt.Fprint(w, "<html>No script</html>")
						return
					}
					count := 1
					if mode == "html-count" {
						count = 2
					}
					fmt.Fprintf(w, `<script>global = {name: "Example", count: %d};global.data = [{"hash":%q}];</script>`, count, hashFor(0))
					return
				}
				switch mode {
				case "missing-details":
					jsonReply(t, w, map[string]any{"error_code": 0, "data": []any{}})
				case "empty-detail-row":
					jsonReply(t, w, map[string]any{"error_code": 0, "data": []any{[]any{}}})
				case "wrong-hash":
					jsonReply(t, w, map[string]any{"error_code": 0, "data": []any{[]any{map[string]any{"songname": "wrong", "audio_info": map[string]any{"hash": hashFor(999)}}}}})
				default:
					t.Error("unexpected detail call")
				}
			})
			if _, err := client.Playlist(context.Background(), "id_123"); err == nil {
				t.Fatal("accepted incomplete source")
			}
		})
	}
}

func TestChainAndShareCodeFamilies(t *testing.T) {
	for _, mode := range []string{"chain-global", "chain-complete", "share-global", "share-wrong-kind", "share-incomplete"} {
		t.Run(mode, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/schain/transfer":
					if mode == "chain-global" {
						jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"global_collection_id": "abc"}})
					} else {
						jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"info": map[string]any{"count": 0, "name": "Empty"}, "list": []any{}}})
					}
				case "/command/":
					kind := 2
					if mode == "share-wrong-kind" {
						kind = 1
					}
					info := map[string]any{"type": kind, "count": 1}
					if mode != "share-incomplete" {
						info["global_collection_id"] = "abc"
					}
					jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"info": info}})
				case "/v2/zlist/list":
					jsonReply(t, w, chainBody(0, []any{}))
				case "/api/v5/special/info_v2":
					jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"songcount": 0}})
				default:
					t.Errorf("unexpected request path %s", r.URL.Path)
				}
			})
			input := "https://m.kugou.com/?chain=abc"
			if strings.HasPrefix(mode, "share-") {
				input = "123"
			}
			list, err := client.Playlist(context.Background(), input)
			if mode == "share-wrong-kind" || mode == "share-incomplete" {
				if err == nil {
					t.Fatal("accepted wrong/incomplete share")
				}
				return
			}
			if err != nil || len(list.Tracks) != 0 {
				t.Fatalf("route: %+v %v", list, err)
			}
		})
	}
}

func TestChainWithoutGlobalIDPaginates(t *testing.T) {
	var pages atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/album_audio/audio" {
			jsonReply(t, w, map[string]any{"error_code": 0, "data": detailRows(t, r)})
			return
		}
		if r.URL.Path != "/schain/transfer" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			return
		}
		pages.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if r.URL.Query().Get("pagesize") != "50" {
			t.Error("changed page size")
		}
		var list []any
		for i := (page - 1) * 50; i < min(page*50, 51); i++ {
			list = append(list, map[string]any{"hash": hashFor(i)})
		}
		jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"info": map[string]any{"name": "Chain", "count": 51}, "list": list}})
	})
	list, err := client.Playlist(context.Background(), "https://m.kugou.com/?chain=abc")
	if err != nil || len(list.Tracks) != 51 || pages.Load() != 2 {
		t.Fatalf("chain pagination: %d tracks, %d pages, %v", len(list.Tracks), pages.Load(), err)
	}
}

func TestSearchOffsetAndFields(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/song_search_v2" || r.URL.Query().Get("pagesize") != "30" || r.URL.Query().Get("keyword") != "测试 & test" {
			t.Error("wrong direct search")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var list []any
		for i := (page - 1) * 30; i < page*30; i++ {
			list = append(list, map[string]any{"FileHash": hashFor(i), "Audioid": i + 1, "OriSongName": fmt.Sprint("Name ", i), "Suffix": "(Live)", "AlbumName": "Album &amp; Test", "Duration": 200, "Singers": []any{map[string]any{"name": "Singer"}}})
		}
		jsonReply(t, w, map[string]any{"error_code": 0, "data": map[string]any{"lists": list, "total": 100}})
	})
	list, err := client.Search(context.Background(), "测试 & test", 4, 28)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(list) != 4 || list[0].ID != hashFor(28) || list[3].ID != hashFor(31) || list[0].Duration != 200 || list[0].Name != "Name 28 (Live)" || list[0].Album != "Album & Test" {
		t.Fatalf("wrong search result %+v", list)
	}
}

func TestSearchValidationCancellationAndSafeErrors(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "sensitive server error", 503) })
	for _, args := range []struct {
		keyword       string
		limit, offset int
	}{{"", 10, 0}, {"x", 0, 0}, {"x", 101, 0}, {"x", 10, -1}, {"x", 10, 10001}} {
		if _, err := client.Search(context.Background(), args.keyword, args.limit, args.offset); err == nil {
			t.Fatal("accepted invalid search")
		}
	}
	if _, err := client.Search(context.Background(), "x", 1, 0); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe/absent HTTP error %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Playlist(ctx, "id_123"); err == nil {
		t.Fatal("accepted canceled context")
	}
	if _, err := client.Search(ctx, "x", 1, 0); err == nil {
		t.Fatal("accepted canceled context")
	}
}

func TestCurrentSpecialMarkupUsesOfficialRouteAndGlobalMetadata(t *testing.T) {
	var official, legacy atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/yy/special/single/123.html":
			official.Add(1)
			fmt.Fprintf(w, `<script>var data=[{"hash":%q}], specialData={specialId:"123"};var specialInfo={"id":123,"name":"Current page","global_collection_id":"collection_test"};</script>`, hashFor(1))
		case "/api/v5/special/info_v2":
			jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"songcount": 1, "specialname": "Authoritative global"}})
		case "/api/v5/special/song_v2":
			jsonReply(t, w, map[string]any{"errcode": 0, "data": map[string]any{"info": []any{map[string]any{"hash": hashFor(1)}}}})
		case "/v3/album_audio/audio":
			jsonReply(t, w, map[string]any{"error_code": 0, "data": detailRows(t, r)})
		default:
			legacy.Add(1)
			http.NotFound(w, r)
		}
	})
	list, err := client.Playlist(context.Background(), "id_123")
	if err != nil || len(list.Tracks) != 1 || list.Name != "Authoritative global" || official.Load() != 1 || legacy.Load() != 0 {
		t.Fatalf("current official route: %+v %v", list, err)
	}
}

func TestLegacySpecialRouteRemainsBoundedFallback(t *testing.T) {
	var pages atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/album_audio/audio" {
			jsonReply(t, w, map[string]any{"error_code": 0, "data": detailRows(t, r)})
			return
		}
		pages.Add(1)
		if r.URL.Path == "/yy/special/single/123.html" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/yueku/v9/special/single/123-5-9999.html" {
			t.Error("unexpected fallback path")
		}
		fmt.Fprintf(w, `<script>global={name:"Legacy",count:1};global.data=[{"hash":%q}];</script>`, hashFor(1))
	})
	list, err := client.Playlist(context.Background(), "id_123")
	if err != nil || len(list.Tracks) != 1 || pages.Load() != 2 {
		t.Fatalf("legacy fallback: %+v %v", list, err)
	}
}

func TestSpecialMarkupRejectsWrongIDsAndAmbiguousAssignments(t *testing.T) {
	for _, body := range []string{
		`<script>var data=[];var data=[];</script>`,
		`<script>var data=[];var specialInfo={"id":456};</script>`,
		`<script>var data=[];var specialInfo={"id":123,"count":1};</script>`,
		`<script>var data=[];var specialInfo={"id":123,"total":1};</script>`,
		`<script>global={name:"Truncated",total:1};global.data=[];</script>`,
		`<script>var data={"wrong":"type"};</script>`,
		`<script>var data=[];var specialInfo={};var specialInfo={};</script>`,
	} {
		if _, _, _, _, err := parseSpecial([]byte(body), "123"); err == nil {
			t.Fatal("accepted ambiguous or inconsistent special page")
		}
	}
}

func TestCountedCompleteSpecialSkipsOtherMetadataRequests(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/yy/special/single/123.html" {
			t.Error("complete counted page made an unnecessary metadata request")
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `<script>var data=[{"hash":%q,"songname":"Song","authors":[{"author_name":"Singer"}],"album_name":"Album","duration":201000}],specialData={};var specialInfo={"id":123,"name":"Counted","songcount":1,"total":1,"global_collection_id":"collection_test"};</script>`, hashFor(1))
	})
	list, err := client.Playlist(context.Background(), "id_123")
	if err != nil || requests.Load() != 1 || list.ReportedCount != 1 || len(list.Tracks) != 1 || list.Tracks[0].Album != "Album" {
		t.Fatalf("complete counted page: %+v, %v, requests=%d", list, err, requests.Load())
	}
}

func TestCurrentSpecialReusesCompleteEmbeddedMetadata(t *testing.T) {
	var gatewayCalls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/yy/special/single/123.html":
			fmt.Fprintf(w, `<script>var data=[{"hash":%q,"songname":"歌曲","authors":[{"author_name":"歌手"}],"album_name":"专辑","timelength":200500}], specialData={specialId:"123"};var specialInfo={"id":123,"name":"公开歌单","global_collection_id":"collection_test"};</script>`, hashFor(1))
		case "/api/v5/special/info_v2":
			http.Error(w, "temporary platform metadata error", 503)
		case "/v3/album_audio/audio":
			gatewayCalls.Add(1)
			http.Error(w, "should not request already available metadata", 500)
		default:
			t.Errorf("unexpected native fallback %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	list, err := client.Playlist(context.Background(), "id_123")
	if err != nil || len(list.Tracks) != 1 || list.Name != "公开歌单" || list.Tracks[0].Name != "歌曲" || list.Tracks[0].Duration != 200 || gatewayCalls.Load() != 0 {
		t.Fatalf("complete embedded metadata not reused: %+v %v", list, err)
	}
}
