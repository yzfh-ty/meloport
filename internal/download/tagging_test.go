package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"go.senan.xyz/taglib"
	"meloport/internal/model"
)

type coverTransport func(*http.Request) (*http.Response, error)

func (f coverTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadEmbeddedTagsAndCover(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var cover bytes.Buffer
	if err := png.Encode(&cover, img); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"flac", "mp3", "m4a", "opus", "ogg", "wav", "aiff", "wv"} {
		t.Run(ext, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("testdata", "tone."+ext))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", strconv.Itoa(len(input)))
				_, _ = w.Write(input)
			}))
			defer server.Close()
			coverCalls := 0
			client := &http.Client{Transport: coverTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "y.gtimg.cn" {
					coverCalls++
					if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
						t.Error("cover request sent credentials")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(cover.Bytes())), ContentLength: int64(cover.Len())}, nil
				}
				return http.DefaultTransport.RoundTrip(r)
			})}
			d := newDownloader(t, Options{HTTPClient: client})
			track := model.Track{Name: "测试曲目", Artists: []string{"艺人甲", "艺人乙"}, Album: "合辑", AlbumArtists: []string{"群星"}, TrackNumber: 3, DiscNumber: 1, Year: "2025", CoverURL: "https://y.gtimg.cn/example.png"}
			dir := t.TempDir()
			got, err := d.Download(context.Background(), model.ResolvedTrack{URL: server.URL, Size: int64(len(input)), Track: track}, dir, "qq-track")
			if err != nil || !got.TagsComplete || got.TagWarning != "" {
				t.Fatalf("download result %+v; %v", got, err)
			}
			tags, err := taglib.ReadTags(got.Path)
			if err != nil {
				t.Fatal(err)
			}
			if want := trackTags(track); !reflect.DeepEqual(selectTags(tags, want), want) {
				t.Fatalf("tags = %#v, want %#v", tags, want)
			}
			art, err := taglib.ReadImage(got.Path)
			if err != nil || !bytes.Equal(art, cover.Bytes()) || coverCalls != 1 {
				t.Fatalf("embedded cover not verified: bytes=%d, calls=%d, error=%v", len(art), coverCalls, err)
			}
			written, err := os.ReadFile(got.Path)
			hash := sha256.Sum256(written)
			if err != nil || got.Size != int64(len(written)) || got.SHA256 != hex.EncodeToString(hash[:]) {
				t.Fatalf("record does not match final bytes: %+v %v", got, err)
			}
			if ext == "mp3" && len(written) >= 128 && string(written[len(written)-128:len(written)-125]) == "TAG" {
				t.Fatal("a generated ID3v1 trailer must not disturb original MP3 padding")
			}
			// Reuse the file and preserve existing tags and artwork, even when
			// the supplied metadata has changed.
			track.Name = "Do not overwrite"
			reused, err := d.Tag(context.Background(), track, got.Path)
			if err != nil || reused != got || coverCalls != 1 {
				t.Fatalf("local repeat changed original tags: %+v %v", reused, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "qq-track."+ext {
				t.Fatalf("staging artefacts remain: %v %v", entries, err)
			}
		})
	}
}

func TestTagCoverFailureKeepsTextAndExistingFields(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "tone.flac"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := taglib.WriteTags(path, map[string][]string{"TITLE": {"我的标题"}, "MUSICBRAINZ_TRACKID": {"fixture-id"}, "LYRICS": {"my lyrics"}}, 0); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: coverTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("not an image")), ContentLength: -1}, nil
	})}
	d := newDownloader(t, Options{HTTPClient: client})
	result, err := d.Tag(context.Background(), model.Track{Name: "普通标题", Artists: []string{"歌手"}, Album: "专辑", CoverURL: "https://y.gtimg.cn/example.png"}, path)
	if err != nil || !result.TagsComplete || !strings.Contains(result.TagWarning, "cover could not") {
		t.Fatalf("result %+v, error %v", result, err)
	}
	tags, err := taglib.ReadTags(path)
	want := map[string][]string{"TITLE": {"我的标题"}, "ARTIST": {"歌手"}, "ALBUM": {"专辑"}, "MUSICBRAINZ_TRACKID": {"fixture-id"}, "LYRICS": {"my lyrics"}}
	if err != nil || !reflect.DeepEqual(selectTags(tags, want), want) {
		t.Fatalf("tags %+v, error %v", tags, err)
	}
}

func selectTags(got, keys map[string][]string) map[string][]string {
	out := make(map[string][]string, len(keys))
	for k := range keys {
		out[k] = got[k]
	}
	return out
}

func TestTagMalformedCoverPreservesOriginalOnCancellation(t *testing.T) {
	var valid bytes.Buffer
	if err := png.Encode(&valid, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	// The header is valid, but the actual image data is incomplete.
	broken := valid.Bytes()[:33]
	if _, _, err := image.DecodeConfig(bytes.NewReader(broken)); err != nil {
		t.Fatalf("test must retain a readable image header: %v", err)
	}
	client := &http.Client{Transport: coverTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: int64(len(broken)), Body: io.NopCloser(bytes.NewReader(broken))}, nil
	})}
	d := newDownloader(t, Options{HTTPClient: client})
	if _, _, err := d.fetchCover(context.Background(), "https://y.gtimg.cn/broken.png"); err == nil {
		t.Fatal("accepted an image with a valid header but damaged content")
	}
	data, err := os.ReadFile(filepath.Join("testdata", "tone.flac"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "track.flac")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Tag(ctx, model.Track{Name: "Not applied"}, path); err == nil {
		t.Fatal("tagging ignored cancellation")
	}
	after, err := os.ReadFile(path)
	entries, listErr := os.ReadDir(dir)
	if err != nil || !bytes.Equal(data, after) || listErr != nil || len(entries) != 1 {
		t.Fatalf("canceled operation altered original or left files: error %v, %v", err, listErr)
	}
}
