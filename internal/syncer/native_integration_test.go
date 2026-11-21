package syncer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.senan.xyz/taglib"

	"meloport/internal/chksz"
	"meloport/internal/download"
	"meloport/internal/model"
	"meloport/internal/platform"
	"meloport/internal/platform/qq"
	"meloport/internal/provider"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

// This is a real native QQ adapter -> native-first Manager -> ChKSz resolver
// -> streaming downloader -> SQLite path; every upstream is a local fixture.
func TestNativeQQPlaylistUsesMeteredAPIOnlyForMissingAudio(t *testing.T) {
	const keyEnv = "MELOPORT_NATIVE_QQ_INTEGRATION_KEY"
	const key = "chksz_native_qq_fixture_not_real"
	t.Setenv(keyEnv, key)
	var nativeCalls, playlistAPICalls, resolveCalls, audioCalls atomic.Int32
	payload := append([]byte("fLaC\x80\x00\x00\x22"), bytes.Repeat([]byte{0, 1, 2, 3}, 512)...)
	var address string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/musicu.fcg":
			nativeCalls.Add(1)
			if r.Method != http.MethodPost || r.URL.Query().Get("apikey") != "" || r.Header.Get("Cookie") != "uin=native_cookie_only" {
				t.Error("native playlist credential isolation failed")
			}
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "bad JSON", 400)
				return
			}
			req, ok := request["req_1"].(map[string]any)
			if !ok || req["module"] != "music.srfDissInfo.aiDissInfo" || req["method"] != "uniform_get_Dissinfo" {
				t.Error("wrong native playlist protocol")
			}
			fmt.Fprint(w, `{"code":0,"req_1":{"code":0,"data":{"total_song_num":2,"dirinfo":{"title":"Direct QQ"},"songlist":[{"id":9001,"mid":"003FirstMID","title":"One","singer":[{"name":"Artist"}],"album":{"name":"Album"},"interval":200},{"id":9002,"mid":"003SecondMID","title":"Two","singer":[{"name":"Artist"}],"album":{"name":"Album"},"interval":210}]}}}`)
		case "/api/163_playlist":
			playlistAPICalls.Add(1)
			t.Error("native playlist unexpectedly used ChKSz metadata")
			http.Error(w, "must not use", 500)
		case "/api/qq_music":
			resolveCalls.Add(1)
			mid := r.URL.Query().Get("mid")
			if (mid != "003FirstMID" && mid != "003SecondMID") || r.URL.Query().Get("apikey") != key || r.URL.Query().Get("size") != "flac" || r.URL.Query().Get("msg") != "" {
				t.Error("resolution used numeric QQ ID or wrong key/quality")
			}
			if r.Header.Get("Cookie") != "" {
				t.Error("native Cookie leaked into ChKSz")
			}
			fmt.Fprintf(w, `{"code":200,"url":%q,"format":"flac","size":%d}`, address+"/audio/"+mid+".flac", len(payload))
		case "/audio/003FirstMID.flac", "/audio/003SecondMID.flac":
			audioCalls.Add(1)
			if r.URL.Query().Get("apikey") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				t.Error("credentials leaked to audio host")
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
	native := qq.New(qq.Options{BaseURL: server.URL, HTTP: platform.NewHTTP(server.Client(), time.Second, 0), Cookie: "uin=native_cookie_only"})
	newManager := func() *provider.Manager {
		return provider.New(map[string]provider.Source{"qq": native}, chksz.NewLazy(chksz.Options{BaseURL: server.URL}, keyEnv), true, nil)
	}
	dl, err := download.New(download.Options{})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	database := filepath.Join(directory, "state.db")
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	task := model.Task{ID: "qq-native", Platform: "qq", PlaylistID: "123", Quality: "flac", Directory: filepath.Join(directory, "music")}
	manager := newManager()
	engine := syncer.NewWithSources(manager, manager, dl, store)
	first, err := engine.Run(context.Background(), task)
	if err != nil || first.Status != "success" || first.MetadataSource != "native" || first.Downloaded != 2 {
		t.Fatalf("initial native sync: %+v %v", first, err)
	}
	if nativeCalls.Load() != 1 || resolveCalls.Load() != 2 || audioCalls.Load() != 2 || playlistAPICalls.Load() != 0 {
		t.Fatalf("wrong call counts: native%d resolve%d audio%d playlistAPI%d", nativeCalls.Load(), resolveCalls.Load(), audioCalls.Load(), playlistAPICalls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(keyEnv, "")
	manager = newManager()
	engine = syncer.NewWithSources(manager, manager, dl, store)
	second, err := engine.Run(context.Background(), task)
	if err != nil || second.Status != "success" || second.Skipped != 2 || second.Downloaded != 0 {
		t.Fatalf("restart without API key: %+v %v", second, err)
	}
	if nativeCalls.Load() != 2 || resolveCalls.Load() != 2 || audioCalls.Load() != 2 || playlistAPICalls.Load() != 0 {
		t.Fatal("restart consumed metered gateway or redownloaded music")
	}
	if err := os.Remove(filepath.Join(task.Directory, "qq-003SecondMID.flac")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(keyEnv, key)
	third, err := engine.Run(context.Background(), task)
	if err != nil || third.Downloaded != 1 || third.Skipped != 1 || third.MetadataSource != "native" {
		t.Fatalf("missing native file repair: %+v %v", third, err)
	}
	if resolveCalls.Load() != 3 || audioCalls.Load() != 3 || playlistAPICalls.Load() != 0 {
		t.Fatal("repair did not limit API usage to the missing song")
	}
	for mid, title := range map[string]string{"003FirstMID": "One", "003SecondMID": "Two"} {
		path := filepath.Join(task.Directory, "qq-"+mid+".flac")
		record, found, err := store.GetDownload(context.Background(), task.ID, "qq", mid)
		if err != nil || !found || record.Path != path || record.SHA256 == "" {
			t.Fatalf("missing QQ identity record: %+v %v", record, err)
		}
		assertTaggedFLAC(t, payload, record, map[string][]string{taglib.Title: {title}, taglib.Artist: {"Artist"}, taglib.Album: {"Album"}})
	}
}

type nativeFixtureSource struct{ list model.Playlist }

func (s nativeFixtureSource) Playlist(context.Context, string) (model.Playlist, error) {
	return s.list, nil
}
func (s nativeFixtureSource) Search(context.Context, string, int, int) ([]model.Track, error) {
	return nil, errors.New("native sync unexpectedly searched instead of reading playlist")
}

func TestNativeSyncKeepsPlatformIdentitiesSeparateAndSkipsWithoutKey(t *testing.T) {
	const keyEnv = "MELOPORT_NATIVE_MULTIPLATFORM_KEY"
	const key = "chksz_native_multi_fixture_not_real"
	t.Setenv(keyEnv, key)
	var resolveCalls, kgSearchCalls, metadataAPICalls, audioCalls atomic.Int32
	payload := append([]byte("fLaC\x80\x00\x00\x22"), bytes.Repeat([]byte{1, 2, 3, 4}, 256)...)
	var address string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/audio/") {
			audioCalls.Add(1)
			if r.URL.Query().Get("apikey") != "" || r.Header.Get("Cookie") != "" {
				t.Error("credentials leaked to audio transport")
			}
			w.Header().Set("Content-Type", "audio/flac")
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.Write(payload)
			return
		}
		if r.URL.Query().Get("apikey") != key {
			t.Error("wrong metered gateway credential")
		}
		if r.URL.Path == "/api/163_playlist" || r.URL.Path == "/api/163_search" {
			metadataAPICalls.Add(1)
			http.Error(w, "unexpected metadata API", 500)
			return
		}
		source := ""
		switch r.URL.Path {
		case "/api/163_music":
			source = "netease"
			if r.URL.Query().Get("id") != "123" || r.URL.Query().Get("level") != "lossless" {
				t.Error("wrong NetEase ID/quality")
			}
		case "/api/qq_music":
			source = "qq"
			if r.URL.Query().Get("mid") != "123" || r.URL.Query().Get("size") != "flac" {
				t.Error("wrong QQ MID/quality")
			}
		case "/api/kugou_music":
			if r.URL.Query().Get("msg") != "" {
				kgSearchCalls.Add(1)
				if r.URL.Query().Get("num") != "50" {
					t.Error("wrong Kugou confirmation limit")
				}
				fmt.Fprint(w, `{"code":200,"list":[{"id":"chkszKG123","name":"Song","singer":"Singer","album":"Album","duration":200,"hash":"AABB"}]}`)
				return
			}
			source = "kugou"
			if r.URL.Query().Get("id") != "chkszKG123" || r.URL.Query().Get("size") != "flac" {
				t.Error("guessed native Kugou ID instead of API's confirmed ID")
			}
		default:
			http.NotFound(w, r)
			return
		}
		resolveCalls.Add(1)
		fmt.Fprintf(w, `{"code":200,"url":%q,"format":"flac","size":%d}`, address+"/audio/"+source+".flac", len(payload))
	}))
	defer server.Close()
	address = server.URL
	sources := map[string]provider.Source{}
	for _, source := range []string{"netease", "qq", "kugou"} {
		track := model.Track{ID: "123", Name: "Song", Artists: []string{"Singer"}, Album: "Album", Duration: 200}
		if source == "kugou" {
			track.Hash = "AABB"
		}
		sources[source] = nativeFixtureSource{list: model.Playlist{ID: "playlist", Name: source, ReportedCount: 1, Tracks: []model.Track{track}}}
	}
	newManager := func() *provider.Manager {
		return provider.New(sources, chksz.NewLazy(chksz.Options{BaseURL: server.URL}, keyEnv), false, nil)
	}
	dl, err := download.New(download.Options{})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	database := filepath.Join(directory, "state.db")
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	manager := newManager()
	engine := syncer.NewWithSources(manager, manager, dl, store)
	for _, source := range []string{"netease", "qq", "kugou"} {
		quality := "flac"
		if source == "netease" {
			quality = "lossless"
		}
		task := model.Task{ID: "same-task", Platform: source, PlaylistID: "playlist", Quality: quality, Directory: filepath.Join(directory, "music")}
		result, err := engine.Run(context.Background(), task)
		if err != nil || result.Downloaded != 1 || result.Status != "success" || result.MetadataSource != "native" {
			t.Fatalf("initial %s sync: %+v %v", source, result, err)
		}
		path := filepath.Join(task.Directory, source+"-123.flac")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("platform file identity lost: %s %v", source, err)
		}
		record, found, err := store.GetDownload(context.Background(), task.ID, source, "123")
		if err != nil || !found || record.Provider != source || record.Path != path {
			t.Fatalf("platform database identity lost: %s %+v %v", source, record, err)
		}
	}
	if resolveCalls.Load() != 3 || kgSearchCalls.Load() != 1 || metadataAPICalls.Load() != 0 || audioCalls.Load() != 3 {
		t.Fatalf("unexpected native sync usage: resolve%d KGsearch%d metadata%d audio%d", resolveCalls.Load(), kgSearchCalls.Load(), metadataAPICalls.Load(), audioCalls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(keyEnv, "")
	manager = newManager()
	engine = syncer.NewWithSources(manager, manager, dl, store)
	for _, source := range []string{"netease", "qq", "kugou"} {
		task := model.Task{ID: "same-task", Platform: source, PlaylistID: "playlist", Directory: filepath.Join(directory, "music")}
		result, err := engine.Run(context.Background(), task)
		if err != nil || result.Skipped != 1 || result.Downloaded != 0 || result.Status != "success" {
			t.Fatalf("keyless cached %s sync: %+v %v", source, result, err)
		}
	}
	if resolveCalls.Load() != 3 || kgSearchCalls.Load() != 1 || metadataAPICalls.Load() != 0 || audioCalls.Load() != 3 {
		t.Fatal("cached native metadata sync consumed API or audio calls without key")
	}
}
