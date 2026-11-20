package chksz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "chksz_local_test_secret"

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Options{BaseURL: server.URL, APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestPlaylistDecodesWrappersWithoutLosingIDPrecision(t *testing.T) {
	const playlist = `{"id":"123","name":"工作歌单","trackCount":1,"tracks":[{"id":9007199254740993,"name":"曲目","ar":[{"name":"歌手 A"},{"name":"歌手 B"}],"al":{"name":"专辑"}}]}`
	for name, body := range map[string]string{
		"direct":   playlist,
		"playlist": `{"code":200,"playlist":` + playlist + `}`,
		"data":     `{"code":0,"data":` + playlist + `}`,
		"nested":   `{"code":200,"data":{"playlist":` + playlist + `}}`,
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/163_playlist" || r.URL.Query().Get("id") != "123" || r.URL.Query().Get("apikey") != testKey {
					t.Error("unexpected request path, ID, or API key")
				}
				fmt.Fprint(w, body)
			})
			got, err := client.Playlist(context.Background(), "123")
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != "工作歌单" || got.ReportedCount != 1 || len(got.Tracks) != 1 {
				t.Fatalf("unexpected playlist: %+v", got)
			}
			track := got.Tracks[0]
			if track.ID != "9007199254740993" || strings.Join(track.Artists, ",") != "歌手 A,歌手 B" || track.Album != "专辑" {
				t.Fatalf("unexpected track: %+v", track)
			}
		})
	}
}

func TestPlaylistAlternateFieldsAndEmptyPlaylist(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") == "2" {
			fmt.Fprint(w, `{"name":"空歌单","songs":[],"song_count":0}`)
			return
		}
		fmt.Fprint(w, `{"playlist_name":"收藏","songs":[{"song_id":"42","song_name":"你好","singer":"歌手","album":"唱片"}],"song_count":"1"}`)
	})
	got, err := client.Playlist(context.Background(), "1")
	if err != nil || len(got.Tracks) != 1 || got.Tracks[0].ID != "42" || got.Tracks[0].Album != "唱片" {
		t.Fatalf("unexpected playlist: %+v, %v", got, err)
	}
	empty, err := client.Playlist(context.Background(), "2")
	if err != nil || len(empty.Tracks) != 0 || empty.ReportedCount != 0 {
		t.Fatalf("unexpected empty playlist: %+v, %v", empty, err)
	}
}

func TestPlaylistRejectsMalformedAndIncompleteResponses(t *testing.T) {
	for name, body := range map[string]string{
		"missing-list":      `{"code":200,"data":{"name":"Missing"}}`,
		"null-list":         `{"tracks":null}`,
		"wrong-list-type":   `{"tracks":{}}`,
		"incomplete":        `{"tracks":[{"id":1}],"trackCount":100}`,
		"count-too-small":   `{"tracks":[{"id":1}],"trackCount":0}`,
		"invalid-count":     `{"tracks":[],"trackCount":-1}`,
		"bad-track":         `{"tracks":[{}]}`,
		"null-track":        `{"tracks":[null]}`,
		"wrong-playlist-id": `{"id":"999","tracks":[]}`,
		"invalid-json":      `not JSON`,
		"trailing-json":     `{"tracks":[]} {"secret":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
			if _, err := client.Playlist(context.Background(), "123"); err == nil {
				t.Fatal("expected an explicit malformed/incomplete response error")
			}
		})
	}
}

func TestPlaylistChecksAuthoritativeTrackIdentities(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"matching-object-IDs", `{"playlist":{"tracks":[{"id":1},{"id":"2"}],"trackIds":[{"id":"2"},{"id":1}]}}`, false},
		{"matching-scalar-IDs", `{"data":{"tracks":[{"id":9007199254740993},{"id":2}],"track_ids":[2,"9007199254740993"]}}`, false},
		{"matching-empty", `{"tracks":[],"trackIds":[]}`, false},
		{"duplicate-track-IDs", `{"tracks":[{"id":1},{"id":1},{"id":2}],"trackCount":3,"trackIds":[1,2]}`, false},
		{"duplicate-authoritative-IDs", `{"tracks":[{"id":1},{"id":2}],"trackIds":[1,1,2]}`, false},
		{"missing-without-count", `{"playlist":{"tracks":[{"id":1}],"trackIds":[{"id":1},{"id":2}]}}`, true},
		{"empty-embedded-tracks", `{"tracks":[],"trackIds":[1]}`, true},
		{"unexpected-embedded-track", `{"tracks":[{"id":1},{"id":2}],"trackIds":[1]}`, true},
		{"equal-count-wrong-IDs", `{"tracks":[{"id":1},{"id":2}],"trackCount":2,"trackIds":[1,3]}`, true},
		{"null-authoritative-array", `{"tracks":[],"trackIds":null}`, true},
		{"object-instead-of-array", `{"tracks":[],"trackIds":{}}`, true},
		{"object-missing-ID", `{"tracks":[{"id":1}],"trackIds":[{}]}`, true},
		{"invalid-ID-string", `{"tracks":[{"id":1}],"track_ids":["not-a-song"]}`, true},
		{"null-ID", `{"tracks":[{"id":1}],"trackIds":[null]}`, true},
		{"zero-ID", `{"tracks":[{"id":1}],"trackIds":[0]}`, true},
		{"fractional-ID", `{"tracks":[{"id":1}],"trackIds":[1.5]}`, true},
		{"contradictory-aliases", `{"tracks":[{"id":1}],"trackIds":[1],"track_ids":[2]}`, true},
		{"outer-total-incomplete", `{"total":100,"data":{"playlist":{"tracks":[{"id":1}]}}}`, true},
		{"outer-IDs-incomplete", `{"trackIds":[1,2],"data":{"playlist":{"tracks":[{"id":1}]}}}`, true},
		{"outer-IDs-wrong-identity", `{"track_ids":[2],"data":{"playlist":{"tracks":[{"id":1}]}}}`, true},
		{"outer-IDs-malformed", `{"trackIds":[{}],"data":{"playlist":{"tracks":[{"id":1}]}}}`, true},
		{"intermediate-count-incomplete", `{"data":{"song_count":2,"playlist":{"tracks":[{"id":1}]}}}`, true},
		{"contradictory-count-aliases", `{"tracks":[{"id":1}],"trackCount":1,"total":2}`, true},
		{"contradictory-wrapper-counts", `{"total":2,"data":{"playlist":{"trackCount":1,"tracks":[{"id":1}]}}}`, true},
		{"malformed-later-count-alias", `{"tracks":[{"id":1}],"trackCount":1,"count":"bad"}`, true},
		{"matching-every-envelope", `{"total":1,"trackIds":[1],"data":{"song_count":"1","track_ids":[{"id":1}],"playlist":{"trackCount":1,"count":1,"tracks":[{"id":1}]}}}`, false},
		{"unrelated-object-ignored", `{"metadata":{"total":100,"trackIds":[2]},"data":{"playlist":{"tracks":[{"id":1}]}}}`, false},
		{"unselected-data-ignored", `{"playlist":{"tracks":[{"id":1}]},"data":{"count":100,"trackIds":[2]}}`, false},
		{"embedded-tracks-stop-traversal", `{"tracks":[{"id":1}],"data":{"count":100,"trackIds":[2]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tc.body)
			})
			_, err := client.Playlist(context.Background(), "123")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Playlist() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestResolveDecodesObjectAndSingleItemDataArray(t *testing.T) {
	for name, body := range map[string]string{
		"direct": `{"url":"https://audio.example.test/song.flac?signature=x","type":"flac","size":1024}`,
		"data":   `{"code":200,"data":{"url":"https://audio.example.test/song.flac?signature=x","format":"flac","size":"1024"}}`,
		"array":  `{"code":200,"data":[{"url":"https://audio.example.test/song.flac?signature=x","size":1024}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/163_music" || r.URL.Query().Get("id") != "42" || r.URL.Query().Get("level") != "lossless" || r.URL.Query().Get("type") != "json" {
					t.Error("unexpected resolve parameters")
				}
				fmt.Fprint(w, body)
			})
			got, err := client.Resolve(context.Background(), "42", "")
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != "https://audio.example.test/song.flac?signature=x" || got.Format != "flac" || got.Size != 1024 {
				t.Fatalf("unexpected resolved track: %+v", got)
			}
		})
	}
}

func TestResolveRejectsUnsafeURLsAndMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"no-url":           `{"data":{}}`,
		"file-url":         `{"url":"file:///etc/passwd"}`,
		"credential-url":   `{"url":"https://user:password@example.test/audio.flac"}`,
		"key-query":        `{"url":"https://example.test/audio?apikey=` + testKey + `"}`,
		"key-path":         `{"url":"https://example.test/` + testKey + `/audio"}`,
		"other-key":        `{"url":"https://example.test/audio?apiKey=another_secret"}`,
		"encoded-key-name": `{"url":"https://example.test/audio?%61pikey=another_secret"}`,
		"bad-size":         `{"url":"https://example.test/audio.flac","size":-1}`,
		"empty-array":      `{"data":[]}`,
		"multi-array":      `{"data":[{"url":"https://a.test/1"},{"url":"https://a.test/2"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
			_, err := client.Resolve(context.Background(), "42", "lossless")
			if err == nil {
				t.Fatal("expected URL/response validation error")
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatal("API key leaked through validation error")
			}
		})
	}
}

func TestHTTPAndBusinessErrorsStopBatchesAndRedactKey(t *testing.T) {
	for _, status := range []int{400, 401, 402, 403, 404, 429, 503} {
		for _, businessError := range []bool{false, true} {
			t.Run(fmt.Sprintf("status-%d-business-%v", status, businessError), func(t *testing.T) {
				var calls atomic.Int32
				client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					if !businessError {
						w.WriteHeader(status)
					}
					fmt.Fprintf(w, `{"code":%d,"msg":"账户错误 %s"}`, status, testKey)
				})
				_, err := client.Playlist(context.Background(), "123")
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != status {
					t.Fatalf("expected APIError %d; got %v", status, err)
				}
				if strings.Contains(err.Error(), testKey) || !strings.Contains(apiErr.Message, "账户错误 [REDACTED]") {
					t.Fatal("upstream message was lost or its credential was exposed")
				}
				stop := status == 401 || status == 402 || status == 403 || status == 429 || status == 503
				if IsAccountError(fmt.Errorf("wrapped: %w", err)) != stop || calls.Load() != 1 {
					t.Fatalf("wrong batch-stop/retry behavior (calls: %d)", calls.Load())
				}
			})
		}
	}
}

func Test429RetriesOnlyOnce(t *testing.T) {
	for _, recovered := range []bool{true, false} {
		t.Run(fmt.Sprintf("recovered-%v", recovered), func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 2 && recovered {
					fmt.Fprint(w, `{"tracks":[]}`)
					return
				}
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"msg":"try later"}`)
			})
			_, err := client.Playlist(context.Background(), "123")
			if (err == nil) != recovered || calls.Load() != 2 {
				t.Fatalf("expected exactly two requests and recovered=%v; calls=%d err=%v", recovered, calls.Load(), err)
			}
		})
	}
}

func Test429BackoffRespectsCancellation(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"msg":"wait"}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := client.Playlist(ctx, "123")
	if !errors.Is(err, context.DeadlineExceeded) || !IsAccountError(err) {
		t.Fatalf("expected canceled account-error backoff; got %v", err)
	}
}

func TestAPIRedirectDoesNotSendCredentialToDestination(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		fmt.Fprint(w, `{"tracks":[]}`)
	}))
	defer destination.Close()
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"?apikey="+testKey, http.StatusFound)
	})
	_, err := client.Playlist(context.Background(), "123")
	if err == nil || destinationCalls.Load() != 0 || strings.Contains(err.Error(), testKey) {
		t.Fatalf("redirect was followed or leaked credentials: calls=%d err=%v", destinationCalls.Load(), err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("failed request containing %s", r.URL.String())
}

func TestTransportErrorsDoNotExposeCredentialURL(t *testing.T) {
	client, err := New(Options{APIKey: testKey, HTTPClient: &http.Client{Transport: failingTransport{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Playlist(context.Background(), "123")
	if err == nil || strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "apikey") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestResponseSizeLimit(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tracks":[],"padding":"`)
		io.Copy(w, io.LimitReader(strings.NewReader(strings.Repeat("a", maxResponseBytes+1)), maxResponseBytes+1))
		fmt.Fprint(w, `"}`)
	})
	_, err := client.Playlist(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("expected size limit error, got %v", err)
	}
}

func TestRequestsAreSerializedAndSpaced(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	var current, maxConcurrent atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		active := current.Add(1)
		defer current.Add(-1)
		if active > maxConcurrent.Load() {
			maxConcurrent.Store(active)
		}
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		fmt.Fprint(w, `{"tracks":[]}`)
	})
	client.minInterval = 40 * time.Millisecond
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Playlist(context.Background(), "123"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if maxConcurrent.Load() != 1 || len(starts) != 3 {
		t.Fatalf("requests not serialized: concurrency=%d count=%d", maxConcurrent.Load(), len(starts))
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < 30*time.Millisecond {
			t.Fatal("requests were not spaced according to interval")
		}
	}
}

func TestRequestsRespectRollingRateWindow(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		fmt.Fprint(w, `{"tracks":[]}`)
	})
	client.rateLimit = 2
	client.rateWindow = 80 * time.Millisecond
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Playlist(context.Background(), "123"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 3 {
		t.Fatalf("expected three requests, got %d", len(starts))
	}
	if starts[2].Sub(starts[0]) < 60*time.Millisecond {
		t.Fatalf("third request started before the rolling window allowed it: %s", starts[2].Sub(starts[0]))
	}
}

func TestRateWindowCancellationDoesNotConsumeSlot(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"tracks":[]}`)
	})
	client.rateLimit = 1
	client.rateWindow = 80 * time.Millisecond
	if _, err := client.Playlist(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Playlist(ctx, "123"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected canceled rate-window wait, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("canceled wait made an unexpected request: %d", calls.Load())
	}
}

func TestFailedRequestConsumesRateWindowSlot(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"msg":"temporary"}`)
	})
	client.rateLimit = 1
	client.rateWindow = 80 * time.Millisecond
	if _, err := client.Playlist(context.Background(), "123"); err == nil {
		t.Fatal("expected first request to fail")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Playlist(ctx, "123"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected second request to wait for the failed request's slot, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("rate window did not count failed request: %d", calls.Load())
	}
}

func Test429RetryConsumesAnotherRateWindowSlot(t *testing.T) {
	var calls atomic.Int32
	var starts []time.Time
	var mu sync.Mutex
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"msg":"try later"}`)
			return
		}
		fmt.Fprint(w, `{"tracks":[]}`)
	})
	client.rateLimit = 1
	client.rateWindow = 80 * time.Millisecond
	if _, err := client.Playlist(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 || starts[1].Sub(starts[0]) < 60*time.Millisecond {
		t.Fatalf("429 retry bypassed the rolling window: starts=%v", starts)
	}
}

func Test429RetryRateWindowCancellationPreservesAccountError(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"msg":"try later"}`)
	})
	client.rateLimit = 1
	client.rateWindow = 80 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := client.Playlist(ctx, "123")
	if !errors.Is(err, context.DeadlineExceeded) || !IsAccountError(err) {
		t.Fatalf("expected canceled retry to remain an account error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("canceled retry made an unexpected request: %d", calls.Load())
	}
}

func TestInvalidOptionsAndRequestParametersDoNotRequest(t *testing.T) {
	for _, opts := range []Options{
		{},
		{BaseURL: "file:///tmp/api", APIKey: testKey},
		{BaseURL: "https://user:pass@example.test", APIKey: testKey},
		{BaseURL: "https://example.test?apikey=secret", APIKey: testKey},
		{APIKey: testKey, MinInterval: -time.Second},
	} {
		if _, err := New(opts); err == nil {
			t.Fatalf("expected invalid options error")
		}
	}
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, id := range []string{"", "0", "123&apikey=x", "https://example.test/123"} {
		if _, err := client.Playlist(context.Background(), id); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	if _, err := client.Resolve(context.Background(), "1", "320k"); err == nil {
		t.Fatal("accepted unsupported quality")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input resulted in an API request")
	}
}

func TestRetryAfterDateAndSeconds(t *testing.T) {
	now := time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"42", 42 * time.Second, true},
		{now.Add(time.Minute).Format(http.TimeFormat), time.Minute, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{"-1", 0, false},
		{"invalid", 0, false},
		{"", 0, false},
	} {
		got, valid := retryAfter(tc.value, now)
		if got != tc.want || valid != tc.valid {
			t.Errorf("retryAfter(%q) = %v, %v; want %v, %v", tc.value, got, valid, tc.want, tc.valid)
		}
	}
	// Ensure percent-encoded query parsing used by credential checks is decoded.
	if parsed, _ := url.Parse("https://example.test/a?%61pikey=x"); parsed.Query().Get("apikey") != "x" {
		t.Fatal("query decoder unexpectedly failed")
	}
}
