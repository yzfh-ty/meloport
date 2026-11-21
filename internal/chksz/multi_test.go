package chksz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestResolvePlatformNativeRoutesAndQuality(t *testing.T) {
	for _, tc := range []struct{ platform, id, quality, endpoint, idKey, qualityKey, expectedQuality string }{
		{"netease", "123", "", "/api/163_music", "id", "level", "lossless"},
		{"netease", "123", "jymaster", "/api/163_music", "id", "level", "jymaster"},
		{"qq", "003NativeMID", "", "/api/qq_music", "mid", "size", "flac"},
		{"qq", "003NativeMID", "320k", "/api/qq_music", "mid", "size", "320k"},
		{"qq", "003NativeMID", "master", "/api/qq_music", "mid", "size", "master"},
		{"kugou", "123456789", "", "/api/kugou_music", "id", "size", "flac"},
		{"kugou", "AABBCCDDEEFF11223344556677889900", "128k", "/api/kugou_music", "id", "size", "128k"},
		{"kugou", "123456789", "hires", "/api/kugou_music", "id", "size", "hires"},
	} {
		t.Run(tc.platform+"/"+tc.expectedQuality, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.Method != http.MethodGet || r.URL.Path != tc.endpoint || q.Get(tc.idKey) != tc.id || q.Get(tc.qualityKey) != tc.expectedQuality || q.Get("type") != "json" || q.Get("apikey") != testKey {
					t.Errorf("incorrect resolution route/parameters: %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("unexpected credential headers")
				}
				io.WriteString(w, `{"code":200,"data":{"url":"https://audio.example/song.flac?signature=opaque","format":"FLAC","size":"12345"}}`)
			})
			audio, err := client.ResolvePlatform(context.Background(), tc.platform, tc.id, tc.quality)
			if err != nil || audio.Format != "flac" || audio.Size != 12345 || !strings.HasSuffix(audio.URL, "signature=opaque") {
				t.Fatalf("unexpected resolution: %+v %v", audio, err)
			}
		})
	}
}

func TestResolvePlatformRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) })
	for _, args := range []struct{ source, id, quality string }{
		{"unknown", "1", ""}, {"qq", "", ""}, {"qq", "../song", ""}, {"qq", "1?apikey=secret", ""}, {"kugou", "-1", ""}, {"kugou", strings.Repeat("a", 129), ""},
		{"qq", "003Valid", "lossless"}, {"qq", "003Valid", "jymaster"}, {"kugou", "123", "standard"}, {"netease", "123", "flac"}, {"netease", "MID", "lossless"},
	} {
		if _, err := client.ResolvePlatform(context.Background(), args.source, args.id, args.quality); err == nil {
			t.Errorf("accepted invalid input: source=%s", args.source)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid input reached upstream %d times", calls.Load())
	}
}

func TestDecodeAudioOnlyKeepsPublicPlatformCover(t *testing.T) {
	client := &Client{key: testKey}
	for _, tc := range []struct{ cover, want string }{
		{"http://y.gtimg.cn/music/photo_new/album.jpg", "https://y.gtimg.cn/music/photo_new/album.jpg"},
		{"https://imge.kugou.com/stdmusic/800/album.jpg", "https://imge.kugou.com/stdmusic/800/album.jpg"},
		{"https://p2.music.126.net/album.jpg?token=nonpublic", ""},
		{"https://y.gtimg.cn/" + testKey + "/album.jpg", ""},
		{"https://y.gtimg.cn.evil.test/album.jpg", ""},
		{"https://y.gtimg.cn:8443/album.jpg", ""},
		{"http://127.0.0.1/album.jpg", ""},
	} {
		result, err := client.decodeAudio(map[string]any{"url": "https://audio.example/song.flac", "cover": tc.cover})
		if err != nil || result.CoverURL != tc.want {
			t.Fatalf("unexpected normalized cover: %q (want %q), %v", result.CoverURL, tc.want, err)
		}
	}
}

func TestResolvePlatformURLAndSizeSafety(t *testing.T) {
	for name, object := range map[string]map[string]any{
		"missing URL":          {},
		"relative URL":         {"url": "/song.flac"},
		"file scheme":          {"url": "file:///etc/passwd"},
		"URL userinfo":         {"url": "https://user:password@audio.example/song.flac"},
		"fragment":             {"url": "https://audio.example/song.flac#secret"},
		"raw key in path":      {"url": "https://audio.example/" + testKey + "/song.flac"},
		"raw key in query":     {"url": "https://audio.example/song.flac?token=" + url.QueryEscape(testKey)},
		"API key param":        {"url": "https://audio.example/song.flac?ApiKey=unrelated"},
		"escaped API key name": {"url": "https://audio.example/song.flac?%61pikey=unrelated"},
		"negative size":        {"url": "https://audio.example/song.flac", "size": -1},
		"fractional size":      {"url": "https://audio.example/song.flac", "size": 1.25},
		"invalid size":         {"url": "https://audio.example/song.flac", "size": "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": object})
			})
			_, err := client.ResolvePlatform(context.Background(), "qq", "003ValidMID", "")
			if err == nil {
				t.Fatal("accepted unsafe audio response")
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "user:password") {
				t.Fatal("credential leaked in error")
			}
		})
	}
	for _, tc := range []struct{ body, format string }{
		{`{"url":"https://audio.example/song.mp3","format":"../../exe"}`, "mp3"},
		{`{"url":"https://audio.example/opaque","format":"../../exe"}`, ""},
		{`{"data":[{"url":"https://audio.example/song","ext":".FLAC"}]}`, "flac"},
	} {
		client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) })
		result, err := client.ResolvePlatform(context.Background(), "kugou", "123", "")
		if err != nil || result.Format != tc.format {
			t.Fatalf("unexpected format: %+v %v", result, err)
		}
	}
}

func TestMultiPlatformSearchRoutesAndMappings(t *testing.T) {
	for _, tc := range []struct {
		source, body, id, keywordKey, endpoint string
		duration                               int
	}{
		{"netease", `{"code":200,"result":{"songs":[{"id":9007199254740993,"name":"Title","ar":[{"name":"Singer"}],"al":{"name":"Album"},"dt":123456}]}}`, "9007199254740993", "keyword", "/api/163_search", 123},
		{"qq", `{"code":0,"count":1,"list":[{"id":123,"mid":"003CorrectMID","name":"Title","singer":"Singer","album":"Album","interval":123}]}`, "003CorrectMID", "msg", "/api/qq_music", 123},
		{"kugou", `{"code":200,"data":{"list":[{"id":"api-own-id_123","name":"Title","singer":[{"name":"Singer"}],"album":{"name":"Album"},"duration":"123","FileHash":"AABB"}]}}`, "api-own-id_123", "msg", "/api/kugou_music", 123},
	} {
		t.Run(tc.source, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.URL.Path != tc.endpoint || q.Get(tc.keywordKey) != "歌名 & Singer" || q.Get("apikey") != testKey {
					t.Error("incorrect search route")
				}
				if tc.source == "netease" {
					if q.Get("limit") != "20" || q.Get("offset") != "0" {
						t.Error("wrong native search paging")
					}
				} else if q.Get("num") != "20" {
					t.Error("wrong fallback search count")
				}
				if q.Get("n") != "" || q.Get("mid") != "" || q.Get("id") != "" {
					t.Error("search unexpectedly selected audio")
				}
				io.WriteString(w, tc.body)
			})
			tracks, err := client.Search(context.Background(), tc.source, "歌名 & Singer", 20, 0)
			if err != nil || len(tracks) != 1 {
				t.Fatalf("unexpected results: %+v %v", tracks, err)
			}
			track := tracks[0]
			if track.ID != tc.id || track.Name != "Title" || !reflect.DeepEqual(track.Artists, []string{"Singer"}) || track.Album != "Album" || track.Duration != tc.duration {
				t.Fatalf("incorrect track mapping: %+v", track)
			}
			if tc.source == "kugou" && track.Hash != "AABB" {
				t.Fatal("lost Kugou hash")
			}
		})
	}
}

func TestSearchFallbackOffsetsAndLimits(t *testing.T) {
	for _, source := range []string{"qq", "kugou"} {
		t.Run(source, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("num") != "5" {
					t.Error("did not request enough prefetched results")
				}
				rows := make([]map[string]any, 5)
				for i := range rows {
					rows[i] = map[string]any{"id": strconv.Itoa(i + 1), "mid": "MID" + strconv.Itoa(i+1), "name": "song"}
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 200, "list": rows})
			})
			tracks, err := client.Search(context.Background(), source, "query", 2, 3)
			if err != nil || len(tracks) != 2 {
				t.Fatalf("wrong sliced results: %+v %v", tracks, err)
			}
			want := "4"
			if source == "qq" {
				want = "MID4"
			}
			if tracks[0].ID != want {
				t.Fatalf("wrong offset: %+v", tracks)
			}
		})
	}
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"list":[]}`) })
	for _, source := range []string{"netease", "qq", "kugou"} {
		tracks, err := client.Search(context.Background(), source, "query", 20, 0)
		if err != nil || tracks == nil || len(tracks) != 0 {
			t.Fatalf("explicit empty failed: %s %+v %v", source, tracks, err)
		}
	}
	tracks, err := client.Search(context.Background(), "qq", "query", 1, 49)
	if err != nil || tracks == nil || len(tracks) != 0 {
		t.Fatalf("out-of-results offset must be empty: %+v %v", tracks, err)
	}
}

func TestSearchFallbackRejectsInvalidInputsAndResponses(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{"list":[]}`) })
	for _, args := range []struct {
		source, keyword string
		limit, offset   int
	}{
		{"unknown", "q", 20, 0}, {"qq", "", 20, 0}, {"qq", "  ", 20, 0}, {"netease", strings.Repeat("a", 301), 20, 0},
		{"netease", "q", 0, 0}, {"netease", "q", 101, 0}, {"netease", "q", 20, -1}, {"netease", "q", 20, 100001},
		{"qq", "q", 51, 0}, {"qq", "q", 2, 49}, {"kugou", "q", 1, 50},
	} {
		if _, err := client.Search(context.Background(), args.source, args.keyword, args.limit, args.offset); err == nil {
			t.Error("accepted invalid search input")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid search reached upstream")
	}
	for name, tc := range map[string]struct{ source, body string }{
		"no list":               {"qq", `{"code":200,"data":{}}`},
		"null list":             {"qq", `{"list":null}`},
		"nonarray":              {"qq", `{"list":{}}`},
		"bad item":              {"qq", `{"list":[null]}`},
		"missing MID":           {"qq", `{"list":[{"id":123,"name":"no mid"}]}`},
		"missing KG ID":         {"kugou", `{"list":[{"hash":"aabb","name":"no id"}]}`},
		"bad ID":                {"kugou", `{"list":[{"id":"../unsafe"}]}`},
		"bad NetEase ID":        {"netease", `{"songs":[{"id":"not-numeric"}]}`},
		"nested business error": {"qq", `{"code":200,"data":{"code":403,"msg":"blocked","list":[]}}`},
		"too deep":              {"qq", `{"data":{"data":{"data":{"data":{"data":{"data":{"list":[]}}}}}}}`},
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) })
			if _, err := client.Search(context.Background(), tc.source, "query", 20, 0); err == nil {
				t.Fatal("accepted malformed fallback response")
			}
		})
	}
}

func TestLazyMissingThenConfiguredKey(t *testing.T) {
	const envName = "MELOPORT_TEST_LAZY_API_KEY"
	t.Setenv(envName, "")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("apikey") != testKey {
			t.Error("wrong lazy credential")
		}
		switch r.URL.Path {
		case "/api/163_playlist":
			io.WriteString(w, `{"tracks":[]}`)
		case "/api/qq_music":
			if r.URL.Query().Get("mid") != "" {
				io.WriteString(w, `{"url":"https://audio.example/song.flac"}`)
			} else {
				io.WriteString(w, `{"list":[]}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	lazy := NewLazy(Options{BaseURL: server.URL, APIKey: "chksz_option_must_not_override_environment"}, envName)
	for _, call := range []func() error{
		func() error { _, err := lazy.Playlist(context.Background(), "123"); return err },
		func() error { _, err := lazy.Search(context.Background(), "qq", "query", 20, 0); return err },
		func() error { _, err := lazy.ResolvePlatform(context.Background(), "qq", "003MID", ""); return err },
	} {
		err := call()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 401 {
			t.Fatalf("wrong absent-key error: %v", err)
		}
		if !strings.Contains(err.Error(), envName) {
			t.Fatal("missing actionable key environment name")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("absent key sent upstream requests")
	}
	t.Setenv(envName, "secret_invalid_key_should_not_echo")
	if _, err := lazy.Search(context.Background(), "qq", "query", 20, 0); err == nil || strings.Contains(err.Error(), "secret_invalid_key_should_not_echo") {
		t.Fatalf("unsafe invalid-key error: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid key sent upstream requests")
	}
	t.Setenv(envName, testKey)
	if _, err := lazy.Playlist(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if _, err := lazy.Search(context.Background(), "qq", "query", 20, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := lazy.ResolvePlatform(context.Background(), "qq", "003MID", ""); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("configured lazy client calls: %d", calls.Load())
	}
}

func TestLazyConcurrentInitializationAndErrorRedaction(t *testing.T) {
	const envName = "MELOPORT_TEST_LAZY_CONCURRENT_KEY"
	t.Setenv(envName, testKey)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("apikey") != testKey {
			t.Error("wrong concurrent key")
		}
		http.Error(w, fmt.Sprintf(`{"msg":%q}`, "rejected "+testKey), http.StatusUnauthorized)
	}))
	defer server.Close()
	lazy := NewLazy(Options{BaseURL: server.URL}, envName)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := lazy.ResolvePlatform(context.Background(), "qq", "003MID", "")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 401 {
				t.Errorf("unexpected error %v", err)
				return
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), server.URL+"/api/") {
				t.Error("credential request leaked")
			}
		}()
	}
	workers.Wait()
	if calls.Load() != 8 {
		t.Fatalf("wrong concurrent request count %d", calls.Load())
	}
}

func TestLazyConfiguredCancellationSendsNoHTTP(t *testing.T) {
	const envName = "MELOPORT_TEST_LAZY_CANCEL_KEY"
	t.Setenv(envName, testKey)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	lazy := NewLazy(Options{BaseURL: server.URL}, envName)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := lazy.ResolvePlatform(ctx, "qq", "003MID", "")
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("canceled lazy call performed work: %v calls%d", err, calls.Load())
	}
}
