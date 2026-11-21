package cloud115

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testToken = "test-only-bearer"

func clientForTest(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(testToken, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL
	return client
}

func TestReadOpenAPI(t *testing.T) {
	const (
		agent = "Player/2.7 (compatible; Console)"
		link  = "https://cdn.example.test/music.flac?expires=42&sign=fixture"
		hash  = "abcdef0123456789abcdef0123456789abcdef01"
	)
	client := clientForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("Accept") != "application/json" {
			t.Error("missing OpenAPI request headers")
		}
		switch r.URL.Path {
		case "/open/user/info":
			if r.Method != http.MethodGet {
				t.Errorf("unexpected user info method %s", r.Method)
			}
			io.WriteString(w, `{"state":true,"code":0,"data":{"user_id":123,"user_name":"音乐库"}}`)
		case "/open/ufile/files":
			query := r.URL.Query()
			if r.Method != http.MethodGet || query.Get("cid") != "92" || query.Get("show_dir") != "1" || query.Get("cur") != "1" || query.Get("limit") != "200" {
				t.Error("incorrect directory listing request")
			}
			switch query.Get("offset") {
			case "0":
				fmt.Fprintf(w, `{"state":true,"code":0,"cid":92,"count":"3","offset":0,"data":[
					{"fid":"55","pid":"92","fc":"0","fn":"专辑 A"},
					{"fid":"56","pid":"92","fc":"1","fn":"歌曲 01.flac","pc":"abc123","fs":"123456","sha1":%q,"aid":"1","fta":"1"}
				]}`, strings.ToUpper(hash))
			case "2":
				io.WriteString(w, `{"state":true,"code":0,"cid":"92","count":3,"offset":"2","data":[
					{"fid":57,"pid":92,"fc":1,"fn":"歌曲 01.mp3","pc":"abc124","fs":100,"fta":2}
				]}`)
			default:
				t.Error("unexpected pagination offset")
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/open/ufile/downurl":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.PostFormValue("pick_code") != "abc123" || r.UserAgent() != agent {
				t.Error("incorrect download address request")
			}
			fmt.Fprintf(w, `{"state":true,"code":0,"data":{"56":{"pick_code":"abc123","url":{"url":%q}}}}`, link)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	if got, err := client.UserInfo(ctx); err != nil || got != (User{ID: "123", Name: "音乐库"}) {
		t.Fatalf("user info = %#v, %v", got, err)
	}
	want := []File{
		{ID: "55", ParentID: "92", Name: "专辑 A", IsDir: true},
		{ID: "56", ParentID: "92", Name: "歌曲 01.flac", PickCode: "abc123", Size: 123456, SHA1: hash},
		{ID: "57", ParentID: "92", Name: "歌曲 01.mp3", PickCode: "abc124", Size: 100, Incomplete: true},
	}
	if got, err := client.List(ctx, "92"); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %#v, %v", got, err)
	}
	if got, err := client.DownURL(ctx, "abc123", agent); err != nil || got != link {
		t.Fatalf("download address = %q, %v", got, err)
	}
}

func TestListingCompleteness(t *testing.T) {
	const file = `{"fid":"5","pid":"0","fn":"file.mp3","fc":"1"}`
	for _, tc := range []struct {
		name   string
		count  int
		first  string
		second string
		ok     bool
	}{
		{name: "empty root", first: "null", ok: true},
		{name: "truncated", count: 2, first: "[" + file + "]", second: "[]"},
		{name: "repeated page", count: 2, first: "[" + file + "]", second: "[" + file + "]"},
		{name: "outside requested root", count: 1, first: `[{"fid":"6","pid":"9","fn":"file.mp3","fc":"1"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := clientForTest(t, func(w http.ResponseWriter, r *http.Request) {
				data := tc.first
				if r.URL.Query().Get("offset") != "0" {
					data = tc.second
				}
				fmt.Fprintf(w, `{"state":true,"count":%d,"cid":"0","data":%s}`, tc.count, data)
			})
			files, err := client.List(context.Background(), "0")
			if tc.ok && (err != nil || len(files) != tc.count) {
				t.Fatalf("valid listing = %v, %v", files, err)
			}
			if !tc.ok && (err == nil || files != nil) {
				t.Fatalf("inconsistent listing was exposed: %v, %v", files, err)
			}
		})
	}
}

func TestRejectInvalidDownloadAddresses(t *testing.T) {
	for _, tc := range []struct{ name, code, url string }{
		{"wrong pick code", "other", "https://cdn.example.test/file.flac"},
		{"credentials", "requested", "https://account@cdn.example.test/file.flac"},
		{"relative path", "requested", "//cdn.example.test/file.flac"},
		{"cleartext HTTP", "requested", "http://cdn.example.test/file.flac"},
		{"local file", "requested", "file:///audio/local.flac"},
		{"header injection", "requested", "https://cdn.example.test/file.flac\r\nHeader: bad"},
		{"bearer echoed", "requested", "https://cdn.example.test/file.flac?value=" + testToken},
		{"encoded bearer echoed", "requested", "https://cdn.example.test/file.flac?value=test%2Donly%2Dbearer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := clientForTest(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"state": true,
					"data": map[string]any{
						"52": map[string]any{"pick_code": tc.code, "url": map[string]string{"url": tc.url}},
					},
				})
			})
			location, err := client.DownURL(context.Background(), "requested", "Player")
			if location != "" || err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), tc.url) {
				t.Fatal("invalid download address was returned or echoed")
			}
		})
	}
}

func TestRequestFailuresDoNotExposeCredentials(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("115 client followed an upstream redirect")
	}))
	t.Cleanup(redirectTarget.Close)
	for _, tc := range []struct {
		name, body, want string
		code             int
		unauthorized     bool
	}{
		{"upstream redirect", "", "HTTP 302", http.StatusFound, false},
		{"HTTP unauthorized", testToken, "supply a fresh access token", http.StatusUnauthorized, true},
		{"expired token with error data", `{"state":false,"code":"40140101","data":[],"message":"` + testToken + `"}`, "supply a fresh access token", http.StatusOK, true},
		{"oversize", strings.Repeat(" ", maxResponseBytes) + testToken, "exceeds 4 MiB", http.StatusOK, false},
		{"malformed JSON", testToken, "response is invalid", http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := clientForTest(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", redirectTarget.URL)
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			})
			_, err := client.DownURL(context.Background(), "requested", "Player")
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), testToken) || errors.Is(err, ErrUnauthorized) != tc.unauthorized {
				t.Fatal("unexpected error or credential leak")
			}
		})
	}
}

func TestSecondPageCancellation(t *testing.T) {
	started := make(chan struct{})
	client := clientForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			io.WriteString(w, `{"state":true,"count":2,"data":[{"fc":"1","fid":"2","pid":"3","fn":"audio.flac"}]}`)
			return
		}
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	files, err := client.List(ctx, "3")
	if files != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list = %v, %v", files, err)
	}
}
