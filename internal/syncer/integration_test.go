package syncer_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"go.senan.xyz/taglib"

	"meloport/internal/chksz"
	"meloport/internal/download"
	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

// This exercises the complete local path. The HTTP responses are fixtures,
// rather than evidence of the undocumented live API's current JSON shape.
func TestFixtureAPIToAudioToSQLiteAndRestart(t *testing.T) {
	const key = "chksz_fixture_not_a_real_key"
	var playlistCalls, resolveCalls, audioCalls atomic.Int32
	payload := append([]byte("fLaC\x80\x00\x00\x22"), bytes.Repeat([]byte{0, 1, 2, 3}, 1024)...)
	var address string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/163_playlist":
			playlistCalls.Add(1)
			if r.URL.Query().Get("apikey") != key {
				t.Error("playlist request did not include configured key")
			}
			fmt.Fprint(w, `{"code":200,"data":{"playlist":{"id":"123","name":"Integration","trackCount":2,"tracks":[{"id":1001,"name":"One","ar":[{"name":"Artist"}]},{"id":"1002","name":"Two"}]}}}`)
		case "/api/163_music":
			resolveCalls.Add(1)
			if r.URL.Query().Get("apikey") != key || r.URL.Query().Get("level") != "lossless" {
				t.Error("resolve request did not include configured key/quality")
			}
			fmt.Fprintf(w, `{"code":200,"data":{"url":%q,"format":"flac","size":%d}}`, address+"/audio/"+r.URL.Query().Get("id")+".flac?token=fixture", len(payload))
		case "/audio/1001.flac", "/audio/1002.flac":
			audioCalls.Add(1)
			if r.URL.Query().Get("apikey") != "" || r.Header.Get("Authorization") != "" {
				t.Error("API credentials leaked to audio transport")
			}
			w.Header().Set("Content-Type", "audio/flac")
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	address = server.URL
	provider, err := chksz.New(chksz.Options{BaseURL: server.URL, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	downloader, err := download.New(download.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	database := filepath.Join(dir, "state.db")
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	task := model.Task{ID: "integration", PlaylistID: "123", Quality: "lossless", Directory: filepath.Join(dir, "music")}
	engine := syncer.New(provider, downloader, store)
	first, err := engine.Run(context.Background(), task)
	if err != nil || first.Downloaded != 2 || first.Status != "success" {
		t.Fatalf("first %+v err=%v", first, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	engine = syncer.New(provider, downloader, store)
	second, err := engine.Run(context.Background(), task)
	if err != nil || second.Skipped != 2 || second.Downloaded != 0 {
		t.Fatalf("restart %+v err=%v", second, err)
	}
	if playlistCalls.Load() != 2 || resolveCalls.Load() != 2 || audioCalls.Load() != 2 {
		t.Fatalf("unnecessary calls after restart: playlist=%d resolve=%d audio=%d", playlistCalls.Load(), resolveCalls.Load(), audioCalls.Load())
	}
	if err := os.Remove(filepath.Join(task.Directory, "netease-1002.flac")); err != nil {
		t.Fatal(err)
	}
	third, err := engine.Run(context.Background(), task)
	if err != nil || third.Skipped != 1 || third.Downloaded != 1 || audioCalls.Load() != 3 {
		t.Fatalf("repair %+v err=%v audio=%d", third, err, audioCalls.Load())
	}
	item, found, err := store.GetDownload(context.Background(), task.ID, "netease", "1002")
	if err != nil || !found {
		t.Fatalf("missing repaired download record: %+v %v", item, err)
	}
	assertTaggedFLAC(t, payload, item, map[string][]string{taglib.Title: {"Two"}})
	runs, err := store.LatestRuns(context.Background(), 3)
	if err != nil || len(runs) != 3 || runs[0].Downloaded != 1 || runs[1].Skipped != 2 {
		t.Fatalf("run history %+v err=%v", runs, err)
	}
}

func assertTaggedFLAC(t *testing.T, source []byte, item state.Download, metadata map[string][]string) {
	t.Helper()
	data, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(flacAudioPayload(t, source), flacAudioPayload(t, data)) {
		t.Fatalf("tagging changed FLAC audio payload at %s", item.Path)
	}
	hash := sha256.Sum256(data)
	if item.Size != int64(len(data)) || item.SHA256 != hex.EncodeToString(hash[:]) || item.TagFingerprint == "" {
		t.Fatalf("database did not record tagged file: %+v", item)
	}
	tags, err := taglib.ReadTags(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range metadata {
		if !slices.Equal(tags[key], want) {
			t.Errorf("FLAC tag %s = %q, want %q", key, tags[key], want)
		}
	}
}

func flacAudioPayload(t *testing.T, data []byte) []byte {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("fLaC")) {
		t.Fatal("missing FLAC signature")
	}
	data = data[4:]
	for len(data) >= 4 {
		last := data[0]&0x80 != 0
		length := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
		if len(data) < 4+length {
			break
		}
		data = data[4+length:]
		if last {
			return data
		}
	}
	t.Fatal("invalid FLAC metadata blocks")
	return nil
}
