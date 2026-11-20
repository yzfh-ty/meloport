package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meloport/internal/platform"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Options{BaseURL: server.URL, HTTP: platform.NewHTTP(server.Client(), time.Second, 0)}), server
}

func testTrack(n int) map[string]any {
	return map[string]any{"id": n + 10000, "mid": fmt.Sprintf("MID%06d", n), "title": fmt.Sprintf("Song %d", n), "singer": []any{map[string]any{"name": "Artist"}}, "album": map[string]any{"name": "Album"}, "interval": 180}
}

func playlistJSON(w http.ResponseWriter, total int, items []any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "req_1": map[string]any{"code": 0, "data": map[string]any{"total_song_num": total, "songlist": items, "dirinfo": map[string]any{"title": "My playlist"}}}})
}

func TestNormalizePlaylist(t *testing.T) {
	valid := map[string]string{
		"123": "123", " 123 ": "123",
		"https://y.qq.com/n/ryqq/playlist/123":                                              "123",
		"https://y.qq.com/n/yqq/playlist/123.html?ADTAG=share":                              "123",
		"https://i.y.qq.com/n2/m/share/details/taoge.html?platform=11&id=123&ADTAG=qfshare": "123",
		"https://y.qq.com/n/m/detail/taoge/index.html?id=123":                               "123",
	}
	for raw, expected := range valid {
		t.Run(raw, func(t *testing.T) {
			id, err := NormalizePlaylist(raw)
			if err != nil || id != expected {
				t.Fatalf("got %q %v", id, err)
			}
		})
	}
	for _, raw := range []string{"", "0", "-1", "0123", "1e10", "https://evil.test/n/ryqq/playlist/123", "https://y.qq.com.evil.test/n/ryqq/playlist/123", "https://a:b@y.qq.com/n/ryqq/playlist/123", "https://y.qq.com:443/n/ryqq/playlist/123", "https://c.y.qq.com/base/fcgi-bin/u?__=short", "https://y.qq.com/n/ryqq/songDetail/123", "https://y.qq.com/n/ryqq/playlist/123#secret", "https://y.qq.com/n/ryqq/playlist/123?access_token=secret", "https://i.y.qq.com/n2/m/share/details/taoge.html?id=123&id=456"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := NormalizePlaylist(raw); err == nil {
				t.Fatal("accepted unsafe or non-playlist URL")
			}
		})
	}
}

func TestPlaylistPagesAndMetadata(t *testing.T) {
	var calls atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/cgi-bin/musicu.fcg" {
			t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Has("apikey") || r.Header.Get("Authorization") != "" {
			t.Error("unexpected API credentials")
		}
		var root map[string]any
		if err := json.NewDecoder(r.Body).Decode(&root); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		req := root["req_1"].(map[string]any)
		if req["module"] != "music.srfDissInfo.aiDissInfo" || req["method"] != "uniform_get_Dissinfo" {
			t.Error("wrong active QQ method")
		}
		params := req["param"].(map[string]any)
		if params["disstid"] != float64(123) || params["song_num"] != float64(100) {
			t.Error("wrong playlist paging contract")
		}
		start := int(params["song_begin"].(float64))
		if start != int(calls.Add(1)-1)*100 {
			t.Errorf("unexpected offset %d", start)
		}
		items := make([]any, 0, 100)
		for i := start; i < min(start+100, 205); i++ {
			items = append(items, testTrack(i))
		}
		playlistJSON(w, 205, items)
	})
	result, err := client.Playlist(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || result.ID != "123" || result.Name != "My playlist" || result.ReportedCount != 205 || len(result.Tracks) != 205 {
		t.Fatalf("wrong playlist: %+v", result)
	}
	track := result.Tracks[0]
	if track.ID != "MID000000" || track.Name != "Song 0" || track.Album != "Album" || len(track.Artists) != 1 || track.Artists[0] != "Artist" || track.Duration != 180 {
		t.Fatalf("bad normalization: %+v", track)
	}
}

func TestPlaylistEmpty(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { playlistJSON(w, 0, []any{}) })
	result, err := client.Playlist(context.Background(), "123")
	if err != nil || result.ReportedCount != 0 || result.Tracks == nil || len(result.Tracks) != 0 {
		t.Fatalf("empty playlist failed: %+v %v", result, err)
	}
}

func TestPlaylistRejectsMalformedAndIncomplete(t *testing.T) {
	for _, mode := range []string{"empty", "short", "repeat", "changed total", "no list", "negative total", "excess total", "too many", "numeric mid", "missing mid", "invalid track", "invalid duration", "outer code", "inner code", "missing code", "missing data", "http status", "invalid json"} {
		t.Run(mode, func(t *testing.T) {
			var calls int
			client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				items := make([]any, 100)
				for i := range items {
					items[i] = testTrack(i + (calls-1)*100)
				}
				total := 101
				switch mode {
				case "empty":
					items = []any{}
				case "short":
					items = items[:50]
				case "repeat":
					for i := range items {
						items[i] = testTrack(i)
					}
				case "changed total":
					if calls > 1 {
						total = 102
						items = items[:2]
					}
				case "negative total":
					total = -1
				case "excess total":
					total = 50001
				case "too many":
					total = 0
					items = items[:1]
				case "numeric mid":
					items[0].(map[string]any)["mid"] = 123
				case "missing mid":
					delete(items[0].(map[string]any), "mid")
				case "invalid track":
					items[0] = "bad"
				case "invalid duration":
					items[0].(map[string]any)["interval"] = -1
				case "outer code":
					io.WriteString(w, `{"code":500001,"msg":"cookie-secret"}`)
					return
				case "inner code":
					io.WriteString(w, `{"code":0,"req_1":{"code":1,"msg":"cookie-secret"}}`)
					return
				case "missing code":
					io.WriteString(w, `{"req_1":{"code":0}}`)
					return
				case "missing data":
					io.WriteString(w, `{"code":0,"req_1":{"code":0}}`)
					return
				case "http status":
					w.WriteHeader(403)
					io.WriteString(w, "cookie-secret")
					return
				case "invalid json":
					io.WriteString(w, "cookie-secret")
					return
				case "no list":
					io.WriteString(w, `{"code":0,"req_1":{"code":0,"data":{"total_song_num":0}}}`)
					return
				}
				playlistJSON(w, total, items)
			})
			_, err := client.Playlist(context.Background(), "123")
			if err == nil {
				t.Fatal("accepted malformed/incomplete playlist")
			}
			if strings.Contains(err.Error(), "cookie-secret") {
				t.Fatal("response credentials leaked")
			}
			if calls > 2 {
				t.Fatalf("unbounded bad-page loop: %d", calls)
			}
		})
	}
}

func TestSearchSigningOffsetAndMetadata(t *testing.T) {
	var calls atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cgi-bin/musics.fcg" || r.Method != "POST" {
			t.Error("search did not use signed platform path")
		}
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.URL.Query().Get("sign") != signRequest(payload) {
			t.Error("signature differs from exact body")
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "QQMusic") {
			t.Error("missing QQ native search user agent")
		}
		var root map[string]any
		if err := json.Unmarshal(payload, &root); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		req := root["music.search.SearchCgiService"].(map[string]any)
		if req["method"] != "DoSearchForQQMusicDesktop" {
			t.Error("wrong active search method")
		}
		params := req["param"].(map[string]any)
		page := int(params["page_num"].(float64))
		if params["query"] != "周杰伦 <test>" || params["num_per_page"] != float64(60) {
			t.Error("bad search parameters")
		}
		if page != int(calls.Add(1)) {
			t.Errorf("unexpected search page %d", page)
		}
		items := make([]any, 60)
		for i := range items {
			items[i] = testTrack((page-1)*60 + i)
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "music.search.SearchCgiService": map[string]any{"code": 0, "data": map[string]any{"body": map[string]any{"song": map[string]any{"list": items}}}}})
	})
	tracks, err := client.Search(context.Background(), "周杰伦 <test>", 65, 55)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(tracks) != 65 || tracks[0].ID != "MID000055" || tracks[64].ID != "MID000119" {
		t.Fatalf("wrong offset window: %d %+v", calls.Load(), tracks)
	}
}

func TestSearchEmptyAndValidation(t *testing.T) {
	var count atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		io.WriteString(w, `{"code":0,"req":{"code":0,"data":{"body":{"song":{"list":[]}}}}}`)
	})
	tracks, err := client.Search(context.Background(), "artist", 20, 0)
	if err != nil || tracks == nil || len(tracks) != 0 {
		t.Fatalf("unexpected empty search %v %+v", err, tracks)
	}
	for _, input := range []struct {
		query         string
		limit, offset int
	}{{"", 20, 0}, {"song", 0, 0}, {"song", 101, 0}, {"song", 20, -1}, {"song", 20, 100001}} {
		if _, err := client.Search(context.Background(), input.query, input.limit, input.offset); err == nil {
			t.Fatal("accepted invalid search parameters")
		}
	}
	if count.Load() != 1 {
		t.Fatal("invalid searches made network requests")
	}
}

func TestSearchRejectsMalformedAndRepeatedPages(t *testing.T) {
	for _, raw := range []string{`{"code":1}`, `{"code":0}`, `{"code":0,"req":{"code":0,"data":{"body":{"song":{"list":null}}}}}`, `{"code":0,"req":{"code":0,"data":{"body":{"song":{"list":[{"id":1}]}}}}}`} {
		client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, raw) })
		if _, err := client.Search(context.Background(), "song", 20, 0); err == nil {
			t.Fatal("accepted malformed search", raw)
		}
	}
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		items := make([]any, 60)
		for i := range items {
			items[i] = testTrack(i)
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "req": map[string]any{"code": 0, "data": map[string]any{"body": map[string]any{"song": map[string]any{"list": items}}}}})
	})
	if _, err := client.Search(context.Background(), "song", 100, 0); err == nil {
		t.Fatal("accepted repeated search page")
	}
}

func TestCookieIsolationCancelAndInvalidBase(t *testing.T) {
	const cookie = "qq_session=local-test-cookie"
	var count atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Header.Get("Cookie") != cookie {
			t.Error("QQ cookie was not sent to configured QQ endpoint")
		}
		if strings.Contains(r.URL.String(), cookie) {
			t.Error("cookie added to URL")
		}
		playlistJSON(w, 0, []any{})
	})
	client.cookie = cookie
	if _, err := client.Playlist(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Playlist(ctx, "123"); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := client.Search(ctx, "song", 20, 0); err == nil {
		t.Fatal("ignored search cancellation")
	}
	if count.Load() != 1 {
		t.Fatal("canceled request reached network")
	}
	bad := New(Options{BaseURL: "https://user:secret@example.test"})
	if _, err := bad.Playlist(context.Background(), "123"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe invalid base error: %v", err)
	}
	if _, err := bad.Search(context.Background(), "song", 20, 0); err == nil {
		t.Fatal("invalid search base accepted")
	}
}

func TestSignatureKnownVectors(t *testing.T) {
	// Values cross-checked by running the public reference's crypto.js.
	for text, expected := range map[string]string{"hello": "zzcfa14dde89n1iwax0l5rimr0qwjexceiov4daaee8d6", "{}": "zzcf8e26805gyafigxmxjehoe02mvsjjgtwzw6f1a05f9"} {
		if got := signRequest([]byte(text)); got != expected {
			t.Fatalf("signature mismatch for %q: %s", text, got)
		}
	}
}
