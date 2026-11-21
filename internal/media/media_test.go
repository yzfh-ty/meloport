package media

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	fixtureMediaID = "4fdQhU5iK2xN6hX9omM9Zw"
	fixtureDirect  = "https://cdn.example.net/audio.flac?signature=short-lived"
)

type lookupFunc struct {
	byID   func(string) (string, bool, error)
	byPath func(string) (string, bool, error)
}

func (l lookupFunc) ByID(_ context.Context, id string) (string, bool, error) {
	return l.byID(id)
}

func (l lookupFunc) ByPath(_ context.Context, path string) (string, bool, error) {
	return l.byPath(path)
}

type resolverFunc func(context.Context, string, string) (string, error)

func (f resolverFunc) DownURL(ctx context.Context, pickCode, userAgent string) (string, error) {
	return f(ctx, pickCode, userAgent)
}

func TestStableMedia(t *testing.T) {
	const pick = "test-pick"
	var resolves int
	lookup := lookupFunc{byID: func(id string) (string, bool, error) {
		switch id {
		case fixtureMediaID:
			return pick, true, nil
		case strings.Repeat("a", 22):
			return "", false, errors.New("private error")
		default:
			return "", false, nil
		}
	}}
	h := New(lookup, resolverFunc(func(_ context.Context, code, ua string) (string, error) {
		resolves++
		if code != pick {
			t.Errorf("Pick code = %q", code)
		}
		switch ua {
		case "test-player":
			return fixtureDirect, nil
		case "unsafe-redirect":
			return "http://untrusted.example.net/private", nil
		default:
			return "", errors.New("private error")
		}
	}), "", "")
	for _, tc := range []struct {
		name, method, route, agent string
		status                     int
	}{
		{"get", http.MethodGet, "/media/" + fixtureMediaID, "test-player", 302},
		{"head with range", http.MethodHead, "/media/" + fixtureMediaID, "test-player", 302},
		{"unknown", http.MethodGet, "/media/" + strings.Repeat("x", 22), "test-player", 404},
		{"malformed", http.MethodGet, "/media/../" + fixtureMediaID, "test-player", 404},
		{"post", http.MethodPost, "/media/" + fixtureMediaID, "test-player", 405},
		{"mapping unavailable", http.MethodGet, "/media/" + strings.Repeat("a", 22), "test-player", 503},
		{"source unavailable", http.MethodGet, "/media/" + fixtureMediaID, "resolver-error", 502},
		{"unsafe location", http.MethodGet, "/media/" + fixtureMediaID, "unsafe-redirect", 502},
		{"isolated", http.MethodGet, "/healthz", "test-player", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(tc.method, tc.route, nil)
			r.Header.Set("User-Agent", tc.agent)
			r.Header.Set("Range", "bytes=123-")
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			location := w.Header().Get("Location")
			if w.Code == 302 {
				if location != fixtureDirect || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Body.Len() != 0 {
					t.Fatal("The response must only expose the temporary redirect in Location")
				}
			} else if location != "" || strings.Contains(w.Body.String(), "private error") {
				t.Fatal("Error response exposed upstream details")
			}
		})
	}
	if resolves != 4 {
		t.Fatalf("Resolve calls = %d, want 4", resolves)
	}
}

func TestProxyRedirectsOnlyAuthenticatedMappedStreams(t *testing.T) {
	var lookups, resolves, songs atomic.Int32
	const path = "/mount/music/netease-2026/歌 曲.flac"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sub/rest/getSong" {
			songs.Add(1)
			if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.ParseForm() != nil || r.Form.Get("f") != "json" {
				t.Error("getSong did not receive authenticated form data")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Form.Get("u") != "player" || r.Form.Get("t") != "valid-token" || r.Form.Get("s") != "salt" {
				_, _ = io.WriteString(w, `{"subsonic-response":{"status":"failed","error":{"code":40},"song":{"id":"mapped","path":"`+path+`"}}}`)
				return
			}
			player, err := r.Cookie("nd-player-706c61796572")
			if err != nil || player.Value != "fixture-player" {
				t.Error("getSong did not preserve the upstream player cookie")
			}
			id := r.Form.Get("id")
			song := map[string]string{"id": id, "path": path}
			switch id {
			case "synthetic":
				song["path"] = "netease-2026/歌 曲.flac"
			case "outside":
				song["path"] = "/mount/music-other/netease-2026/歌 曲.flac"
			case "wrong-id":
				song["id"] = "mapped"
			case "unmapped":
				song["path"] = "/mount/music/netease-2026/other.flac"
			case "profile":
				song["transcodedSuffix"] = "mp3"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok", "song": song}})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/sub/") {
			t.Errorf("Proxy lost configured base path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Method", r.Method)
		w.Header().Set("X-Echo-Query", r.URL.RawQuery)
		w.Header().Set("X-Echo-Range", r.Header.Get("Range"))
		_, _ = w.Write([]byte("upstream:" + string(body)))
	}))
	t.Cleanup(upstream.Close)
	h := New(lookupFunc{byPath: func(path string) (string, bool, error) {
		lookups.Add(1)
		return "web-pick", path == "netease-2026/歌 曲.flac", nil
	}}, resolverFunc(func(_ context.Context, code, ua string) (string, error) {
		resolves.Add(1)
		if code != "web-pick" || ua != "test-player" {
			t.Error("Missing mapped pick code or original player User-Agent")
		}
		return fixtureDirect, nil
	}), upstream.URL+"/sub", "/mount/music")
	const auth = "u=player&t=valid-token&s=salt&v=1.16.1&c=test&f=xml"
	for _, tc := range []struct {
		name, method, path, query, body string
		wantRedirect                    bool
	}{
		{"query auth", http.MethodGet, "/rest/stream.view", auth + "&id=mapped", "", true},
		{"form auth", http.MethodPost, "/rest/stream", "id=mapped", auth + "&format=raw", true},
		{"failed auth", http.MethodGet, "/rest/stream", strings.ReplaceAll(auth, "valid-token", "expired") + "&id=mapped", "", false},
		{"synthetic path", http.MethodGet, "/rest/stream", auth + "&id=synthetic", "", false},
		{"outside mount", http.MethodGet, "/rest/stream", auth + "&id=outside", "", false},
		{"wrong song", http.MethodGet, "/rest/stream", auth + "&id=wrong-id", "", false},
		{"unmapped", http.MethodGet, "/rest/stream", auth + "&id=unmapped", "", false},
		{"transcoding profile", http.MethodGet, "/rest/stream", auth + "&id=profile", "", false},
		{"explicit raw override", http.MethodGet, "/rest/stream", auth + "&id=profile&format=raw", "", true},
		{"form transcode", http.MethodPost, "/rest/stream", "id=mapped", auth + "&format=mp3", false},
		{"bitrate limit", http.MethodGet, "/rest/stream", auth + "&id=mapped&maxBitRate=128", "", false},
		{"time offset", http.MethodGet, "/rest/stream", auth + "&id=mapped&timeOffset=5", "", false},
		{"ambiguous form", http.MethodPost, "/rest/stream", "id=mapped", auth + "&id=mapped", false},
		{"malformed form", http.MethodPost, "/rest/stream", "id=mapped", auth + "&format=%Q", false},
		{"large form", http.MethodPost, "/rest/stream", "id=mapped", auth + "&padding=" + strings.Repeat("x", maxFormSize), false},
		{"HLS route", http.MethodGet, "/rest/getTranscodeStream.view", auth + "&mediaId=mapped", "", false},
		{"normal upstream GET", http.MethodGet, "/rest/ping", auth, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path+"?"+tc.query, strings.NewReader(tc.body))
			r.Header.Set("User-Agent", "test-player")
			r.Header.Set("Range", "bytes=42-")
			r.AddCookie(&http.Cookie{Name: "nd-player-706c61796572", Value: "fixture-player"})
			if tc.method == http.MethodPost {
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if tc.wantRedirect {
				if w.Code != 302 || w.Header().Get("Location") != fixtureDirect || w.Body.Len() != 0 {
					t.Fatalf("Expected CDN redirect, got %d", w.Code)
				}
				return
			}
			if w.Code != 200 || w.Header().Get("Location") != "" || w.Header().Get("X-Echo-Method") != tc.method || w.Header().Get("X-Echo-Query") != tc.query ||
				w.Header().Get("X-Echo-Range") != "bytes=42-" || w.Body.String() != "upstream:"+tc.body {
				t.Fatalf("Fallback changed the proxied request, status = %d", w.Code)
			}
		})
	}
	if resolves.Load() != 3 || lookups.Load() != 4 || songs.Load() != 9 {
		t.Fatalf("Unexpected calls: getSong %d, mapping %d, resolver %d", songs.Load(), lookups.Load(), resolves.Load())
	}
}

func TestProxyDoesNotFollowCredentialsAcrossGetSongRedirect(t *testing.T) {
	var unexpected atomic.Bool
	otherHost := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		unexpected.Store(true)
	}))
	t.Cleanup(otherHost.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/getSong" {
			http.Redirect(w, r, otherHost.URL, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
	}))
	t.Cleanup(upstream.Close)
	h := New(lookupFunc{}, nil, upstream.URL, "/mount")
	query := url.Values{"u": {"player"}, "p": {"test-only"}, "id": {"mapped"}, "c": {"test"}, "v": {"1.16.1"}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/rest/stream?"+query.Encode(), nil)
	r.AddCookie(&http.Cookie{Name: "nd-player-706c61796572", Value: "fixture-player"})
	h.ServeHTTP(w, r)
	if unexpected.Load() || w.Code != http.StatusPartialContent {
		t.Fatalf("Expected fallback to upstream stream, got %d", w.Code)
	}
}

func TestMountedPathRequiresAnAbsoluteDescendant(t *testing.T) {
	for _, tc := range []struct{ root, song, want string }{
		{"/music/115/", "/music/115/task/歌 曲.flac", "task/歌 曲.flac"},
		{`G:\Music\115`, `g:\music\115\task\歌 曲.flac`, "task/歌 曲.flac"},
		{`G:/Music/115`, `G:\Music\115\task\test.flac`, "task/test.flac"},
		{"/music/115", "task/song.flac", ""},
		{"/music/115", "/music/115other/task/song.flac", ""},
		{"/music/115", "/music/115/../task/song.flac", ""},
		{"/music/115", `/music/115/task\song.flac`, ""},
		{"/music/115", "/music/115/", ""},
		{`G:\Music\115`, `H:\Music\115\task\song.flac`, ""},
		{`G:\Music\115`, `G:\Music\115\task\song.flac:stream`, ""},
	} {
		relative, ok := mountedPath(tc.root, tc.song)
		if ok != (tc.want != "") || relative != tc.want {
			t.Errorf("mount %q song %q = %q (%t), want %q", tc.root, tc.song, relative, ok, tc.want)
		}
	}
	for _, invalid := range []string{"", "relative/path", `C:relative`, `\\server\share`, "/mount/../music", "//", `C:\\`, "/a//b"} {
		if ValidMountRoot(invalid) {
			t.Errorf("Accepted invalid mount root %q", invalid)
		}
	}
}
