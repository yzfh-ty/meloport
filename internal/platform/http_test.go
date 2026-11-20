package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testMetadataHTTP(t *testing.T, handler http.HandlerFunc) (*HTTP, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewHTTP(server.Client(), 2*time.Second, 0), server.URL
}

func TestHTTPJSONPreservesLargeIDsAndRejectsBadDocuments(t *testing.T) {
	tests := []struct {
		name, body string
		valid      bool
	}{
		{"large-id", `{"id":9007199254740993123456789,"nested":{"count":123}}`, true},
		{"empty-object", `{}`, true},
		{"null", `null`, false},
		{"array", `[{"id":1}]`, false},
		{"string", `"not an object"`, false},
		{"bad-json", `{"id":`, false},
		{"trailing-json", `{"id":1} {"id":2}`, false},
		{"trailing-text", `{"id":1} sensitive`, false},
		{"duplicate-key", `{"code":500,"code":0}`, false},
		{"nested-duplicate-key", `{"data":{"total":2,"total":1}}`, false},
		{"escaped-duplicate-key", `{"data":1,"\u0064ata":2}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, test.body) })
			result, err := client.JSON(context.Background(), http.MethodGet, address, nil, nil)
			if !test.valid {
				if err == nil {
					t.Fatal("accepted invalid or ambiguous JSON")
				}
				if strings.Contains(err.Error(), test.body) {
					t.Fatal("raw body leaked")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "large-id" {
				if Text(result, "id") != "9007199254740993123456789" {
					t.Fatal("ID lost precision")
				}
				if _, ok := result["id"].(json.Number); !ok {
					t.Fatal("ID was not json.Number")
				}
			}
		})
	}
}

func TestHTTPJSONAllowsOnlyExplicitDuplicateKeys(t *testing.T) {
	client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"updateFrequency":null,"updateFrequency":"每天","id":1}`)
	})
	result, err := client.JSONAllowDuplicateKeys(context.Background(), http.MethodGet, address, nil, nil, "updateFrequency")
	if err != nil || Text(result, "updateFrequency") != "每天" {
		t.Fatalf("explicit duplicate allowance failed: %+v %v", result, err)
	}
	client, address = testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"id":2}`)
	})
	if _, err := client.JSONAllowDuplicateKeys(context.Background(), http.MethodGet, address, nil, nil, "updateFrequency"); err == nil {
		t.Fatal("accepted a duplicate field outside the allowance")
	}
}

func TestHTTPResponseLimitAndBoundedText(t *testing.T) {
	for _, method := range []string{"json", "bytes", "text"} {
		t.Run(method, func(t *testing.T) {
			client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				block := strings.Repeat("x", 64*1024)
				for i := 0; i <= MaxResponseBytes/len(block); i++ {
					if _, err := io.WriteString(w, block); err != nil {
						return
					}
				}
			})
			var err error
			switch method {
			case "json":
				_, err = client.JSON(context.Background(), http.MethodGet, address, nil, nil)
			case "bytes":
				_, err = client.Bytes(context.Background(), http.MethodGet, address, nil, nil)
			case "text":
				_, err = client.TextResponse(context.Background(), http.MethodGet, address, nil, nil)
			}
			if err == nil || !strings.Contains(err.Error(), "16 MiB") {
				t.Fatalf("missing response limit: %v", err)
			}
		})
	}
	client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<script>metadata only</script>") })
	text, err := client.TextResponse(context.Background(), http.MethodGet, address, nil, nil)
	if err != nil || text != "<script>metadata only</script>" {
		t.Fatal("bounded raw HTML unavailable", err)
	}
}

type failingMetadataTransport struct{}

func (failingMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unsafe transport diagnostic: %s Cookie=%s", r.URL.String(), r.Header.Get("Cookie"))
}

func TestHTTPSafeErrorsAndURLValidation(t *testing.T) {
	const secret = "chksz_native_error_test_not_real"
	client := NewHTTP(&http.Client{Transport: failingMetadataTransport{}}, time.Second, 0)
	_, err := client.JSON(context.Background(), http.MethodGet, "https://native.test/playlist?token="+secret, nil, http.Header{"Cookie": {"session=" + secret}})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "native.test") {
		t.Fatalf("unsafe transport error: %v", err)
	}
	for _, address := range []string{"ftp://native.test/data", "https://user:{{PASSWORD_jjbm0uqg}}@native.test/data", "://invalid", "https:///missing-host"} {
		if _, err := client.JSON(context.Background(), http.MethodGet, address, nil, nil); err == nil {
			t.Fatal("accepted unsafe URL")
		}
	}
	client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "session="+secret, http.StatusForbidden) })
	_, err = client.JSON(context.Background(), http.MethodGet, address+"?token="+secret, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe HTTP status error: %v", err)
	}
}

func TestHTTPDoesNotFollowRedirectsOrUseCookieJar(t *testing.T) {
	var redirects atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); fmt.Fprint(w, `{}`) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("HTTP client cookie jar leaked into metadata request")
		}
		http.Redirect(w, r, destination.URL+"?token=never-follow", http.StatusFound)
	}))
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(server.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "should-not-send"}})
	custom := server.Client()
	custom.Jar = jar
	client := NewHTTP(custom, time.Second, 0)
	_, err = client.JSON(context.Background(), http.MethodGet, server.URL, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "302") || redirects.Load() != 0 {
		t.Fatalf("redirect followed: %d %v", redirects.Load(), err)
	}
	if custom.Jar == nil {
		t.Fatal("constructor mutated caller's HTTP client")
	}
}

func TestHTTPPreservesProtocolHeaders(t *testing.T) {
	client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("User-Agent") != "Android-Native-Protocol" || r.Header.Get("Referer") != "https://owned-platform.test/" || r.Header.Get("Cookie") != "user-supplied-platform-cookie" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("protocol headers changed")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"playlist":1}` {
			t.Error("payload changed", err)
		}
		fmt.Fprint(w, `{"code":0}`)
	})
	headers := http.Header{"User-Agent": {"Android-Native-Protocol"}, "Referer": {"https://owned-platform.test/"}, "Cookie": {"user-supplied-platform-cookie"}, "Content-Type": {"application/json"}}
	if _, err := client.JSON(context.Background(), http.MethodPost, address, []byte(`{"playlist":1}`), headers); err != nil {
		t.Fatal(err)
	}
	if headers.Get("Accept") != "" {
		t.Fatal("constructor/request mutated input headers")
	}
}

func TestHTTPGateWaitAndDelayAreCancelable(t *testing.T) {
	t.Run("gate", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var requests atomic.Int32
		client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			close(entered)
			<-release
			fmt.Fprint(w, `{}`)
		})
		finished := make(chan error, 1)
		go func() {
			_, err := client.JSON(context.Background(), http.MethodGet, address, nil, nil)
			finished <- err
		}()
		<-entered
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := client.JSON(ctx, http.MethodGet, address, nil, nil)
		close(release)
		if firstErr := <-finished; firstErr != nil {
			t.Fatal(firstErr)
		}
		if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 {
			t.Fatalf("gate cancellation: %d %v", requests.Load(), err)
		}
	})
	t.Run("delay", func(t *testing.T) {
		var requests atomic.Int32
		client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, `{}`) })
		client.interval = time.Second
		if _, err := client.JSON(context.Background(), http.MethodGet, address, nil, nil); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := client.JSON(ctx, http.MethodGet, address, nil, nil)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 500*time.Millisecond || requests.Load() != 1 {
			t.Fatalf("delay cancellation: %d %v", requests.Load(), err)
		}
	})
}

func TestHTTPSerializesAndSpacesNativeRequests(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	var active, maxActive atomic.Int32
	client, address := testMetadataHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		count := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if count <= old || maxActive.CompareAndSwap(old, count) {
				break
			}
		}
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		fmt.Fprint(w, `{}`)
	})
	client.interval = 40 * time.Millisecond
	var wg sync.WaitGroup
	errorsCh := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.JSON(context.Background(), http.MethodGet, address, nil, nil)
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if maxActive.Load() != 1 || len(starts) != 4 {
		t.Fatalf("requests overlapped: %d active, %d starts", maxActive.Load(), len(starts))
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 30*time.Millisecond {
			t.Fatalf("native request spacing too short: %s", gap)
		}
	}
}
