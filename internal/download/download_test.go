package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meloport/internal/model"
)

func flacFixture() []byte {
	return append([]byte("fLaC\x80\x00\x00\x22"), bytes.Repeat([]byte{0, 1, 2, 3}, 1024)...)
}

func newDownloader(t *testing.T, opts Options) *Downloader {
	t.Helper()
	d, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func assertNoFiles(t *testing.T, directory string) {
	t.Helper()
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("failed download left files: %v", files)
	}
}

func TestDownloadAndRecoverWithoutNetwork(t *testing.T) {
	payload := flacFixture()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected authentication sent to audio server")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	directory := t.TempDir()
	d := newDownloader(t, Options{})
	audio := model.ResolvedTrack{URL: server.URL + "/untrusted.exe?token=secret", Format: "../../mp3", Size: int64(len(payload))}
	got, err := d.Download(context.Background(), audio, directory, "netease-123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != filepath.Join(directory, "netease-123.flac") || got.Size != int64(len(payload)) {
		t.Fatalf("unexpected result: %+v", got)
	}
	hash := sha256.Sum256(payload)
	if got.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("incorrect checksum: %s", got.SHA256)
	}
	contents, err := os.ReadFile(got.Path)
	if err != nil || !bytes.Equal(contents, payload) {
		t.Fatalf("incorrect saved audio: %v", err)
	}
	stat, err := os.Stat(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := d.Download(context.Background(), audio, directory, "netease-123")
	if err != nil || recovered != got || calls.Load() != 1 {
		t.Fatalf("recovery performed network I/O or changed result: %+v, %v, calls=%d", recovered, err, calls.Load())
	}
	after, err := os.Stat(got.Path)
	if err != nil || !after.ModTime().Equal(stat.ModTime()) {
		t.Fatal("existing audio was overwritten")
	}
	files, _ := os.ReadDir(directory)
	if len(files) != 1 {
		t.Fatalf("temporary file leaked: %v", files)
	}
}

func TestDownloadRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		payload     []byte
		maxBytes    int64
		expected    int64
		chunked     bool
	}{
		{name: "html", contentType: "text/html", payload: []byte("<html>sign in</html>")},
		{name: "json", contentType: "application/problem+json", payload: []byte(`{"error":"no permission"}`)},
		{name: "fake audio header", contentType: "audio/flac", payload: []byte("<html>this is not audio</html>")},
		{name: "fake octet stream", contentType: "application/octet-stream", payload: []byte("Access denied")},
		{name: "empty", contentType: "audio/flac"},
		{name: "non success", status: http.StatusForbidden, payload: flacFixture()},
		{name: "partial content", status: http.StatusPartialContent, payload: flacFixture()},
		{name: "content length oversized", payload: flacFixture(), maxBytes: 64},
		{name: "chunked oversized", payload: flacFixture(), maxBytes: 64, chunked: true},
		{name: "metadata size mismatch", payload: flacFixture(), expected: 12},
		{name: "chunked metadata size mismatch", payload: flacFixture(), expected: 12, chunked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				if tt.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write(tt.payload)
			}))
			defer server.Close()
			directory := t.TempDir()
			d := newDownloader(t, Options{MaxBytes: tt.maxBytes})
			_, err := d.Download(context.Background(), model.ResolvedTrack{URL: server.URL + "/audio?token=do-not-leak", Size: tt.expected}, directory, "netease-1")
			if err == nil {
				t.Fatal("expected response rejection")
			}
			if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("signed URL leaked: %v", err)
			}
			assertNoFiles(t, directory)
		})
	}
}

func TestDownloadTruncatedTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		w.Header().Set("Content-Length", "20000")
		_, _ = w.Write(flacFixture())
	}))
	defer server.Close()
	directory := t.TempDir()
	_, err := newDownloader(t, Options{}).Download(context.Background(), model.ResolvedTrack{URL: server.URL}, directory, "netease-1")
	if err == nil {
		t.Fatal("truncated audio accepted")
	}
	assertNoFiles(t, directory)
}

func TestDownloadCancellationRemovesPartialFile(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(flacFixture())
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := newDownloader(t, Options{})
	finished := make(chan error, 1)
	go func() {
		_, err := d.Download(ctx, model.ResolvedTrack{URL: server.URL}, directory, "netease-1")
		finished <- err
	}()
	<-started
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled download did not finish")
	}
	assertNoFiles(t, directory)
}

func TestExistingInvalidFileIsPreserved(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "netease-1.flac")
	payload := []byte("broken user file")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newDownloader(t, Options{}).Download(context.Background(), model.ResolvedTrack{URL: "https://example.invalid/audio"}, directory, "netease-1")
	if err == nil || !strings.Contains(err.Error(), "preserved") {
		t.Fatalf("expected explicit preservation error, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, payload) {
		t.Fatal("invalid existing file was modified")
	}
}

func TestUnsafeInputs(t *testing.T) {
	d := newDownloader(t, Options{})
	for _, filename := range []string{"", "../escape", "..", "one/two", `one\two`, "name:stream", "name.", "CON", "nul.mp3"} {
		t.Run(filename, func(t *testing.T) {
			directory := t.TempDir()
			_, err := d.Download(context.Background(), model.ResolvedTrack{URL: "https://example.invalid"}, directory, filename)
			if err == nil {
				t.Fatal("unsafe filename accepted")
			}
			assertNoFiles(t, directory)
		})
	}
	for _, rawURL := range []string{"ftp://example.org/a", "https://user:secret@example.org/a", "/relative", "http://", ":\nsecret"} {
		_, err := d.Download(context.Background(), model.ResolvedTrack{URL: rawURL}, t.TempDir(), "netease-1")
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe URL accepted or leaked: %v", err)
		}
	}
}

func TestRequestFailureDoesNotLeakSignedURL(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("upstream signed token=secret-sensitive-data")
	})}
	d := newDownloader(t, Options{HTTPClient: client})
	_, err := d.Download(context.Background(), model.ResolvedTrack{URL: "https://example.org/audio?apikey=secret-key"}, t.TempDir(), "netease-1")
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.org") {
		t.Fatalf("signed URL leaked: %v", err)
	}
}

func TestRedirectHasNoCookiesAndKeepsCallerClientUnchanged(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" {
			t.Error("authentication leaked across audio redirect")
		}
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(flacFixture())
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	jar, _ := cookiejar.New(nil)
	parsed, _ := url.Parse(source.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: "session", Value: "secret"}})
	client := &http.Client{Jar: jar, Timeout: 42 * time.Second}
	d := newDownloader(t, Options{HTTPClient: client})
	if _, err := d.Download(context.Background(), model.ResolvedTrack{URL: source.URL}, t.TempDir(), "netease-1"); err != nil {
		t.Fatal(err)
	}
	if client.Jar != jar || client.Timeout != 42*time.Second || client.CheckRedirect != nil {
		t.Fatal("New mutated caller's HTTP client")
	}
}

func TestRedirectLoopIsBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/again?secret=value", http.StatusFound)
	}))
	defer server.Close()
	directory := t.TempDir()
	_, err := newDownloader(t, Options{}).Download(context.Background(), model.ResolvedTrack{URL: server.URL}, directory, "netease-1")
	if err == nil || calls.Load() > 6 {
		t.Fatalf("redirects unbounded: error=%v calls=%d", err, calls.Load())
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("redirect URL leaked")
	}
	assertNoFiles(t, directory)
}

func TestConcurrentPublishDoesNotOverwrite(t *testing.T) {
	payload := flacFixture()
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			close(release)
		}
		<-release
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	d := newDownloader(t, Options{})
	directory := t.TempDir()
	var group sync.WaitGroup
	results := make(chan model.DownloadResult, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := d.Download(context.Background(), model.ResolvedTrack{URL: server.URL}, directory, "netease-1")
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		}()
	}
	group.Wait()
	close(results)
	var first model.DownloadResult
	for result := range results {
		if first.Path == "" {
			first = result
		} else if first != result {
			t.Fatalf("concurrent writers disagree: %+v and %+v", first, result)
		}
	}
	files, _ := os.ReadDir(directory)
	if len(files) != 1 {
		t.Fatalf("concurrent transfer left files: %v", files)
	}
}

func TestDetectFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"flac", flacFixture(), "flac"},
		{"mp3 ID3", []byte("ID3\x04\x00\x00\x00\x00\x00\x00audio"), "mp3"},
		{"mp3 frame", []byte{0xff, 0xfb, 0x90, 0, 1, 2, 3, 4}, "mp3"},
		{"ADTS", []byte{0xff, 0xf1, 0x50, 0, 1, 2, 3, 4}, "aac"},
		{"m4a", []byte("\x00\x00\x00\x18ftypM4A audio"), "m4a"},
		{"ogg", []byte("OggS\x00\x00vorbis audio"), "ogg"},
		{"opus", []byte("OggS\x00\x00OpusHead audio"), "opus"},
		{"wav", []byte("RIFF\x00\x00\x00\x00WAVEaudio"), "wav"},
		{"aiff", []byte("FORM\x00\x00\x00\x00AIFFaudio"), "aiff"},
		{"ape", []byte("MAC audio"), "ape"},
		{"wavpack", []byte("wvpkaudio"), "wv"},
		{"arbitrary", []byte("not audio"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectFormat(tc.data); got != tc.want {
				t.Fatalf("detectFormat=%q want %q", got, tc.want)
			}
		})
	}
}

func TestOptionsValidation(t *testing.T) {
	for _, opts := range []Options{{Timeout: -time.Second}, {MaxBytes: -1}, {MaxBytes: int64(^uint64(0) >> 1)}} {
		if _, err := New(opts); err == nil {
			t.Fatalf("invalid options accepted: %+v", opts)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFailedReadCleansTemporaryFile(t *testing.T) {
	header := flacFixture()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: -1,
			Header:        http.Header{"Content-Type": []string{"audio/flac"}},
			Body:          io.NopCloser(io.MultiReader(bytes.NewReader(header), brokenReader{})),
		}, nil
	})}
	directory := t.TempDir()
	_, err := newDownloader(t, Options{HTTPClient: client}).Download(context.Background(), model.ResolvedTrack{URL: "https://example.org/audio?token=secret"}, directory, "netease-1")
	if err == nil {
		t.Fatal("interrupted transfer accepted")
	}
	assertNoFiles(t, directory)
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("connection interrupted token=secret")
}
