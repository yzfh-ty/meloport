package app

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.senan.xyz/taglib"

	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

type navidromeLibraryFixture struct {
	name             string
	path             string
	ids              []string
	creates, updates int
	searches         int
}

func navidromeLibrary(t *testing.T, songPath string) (config.Cloud115, *navidromeLibraryFixture) {
	t.Helper()
	library := &navidromeLibraryFixture{path: songPath}
	t.Setenv("MELOPORT_NAVIDROME_FIXTURE_PASSWORD", "fixture-password")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RawQuery != "" {
			t.Error("Navidrome publication must use POST bodies")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		digest := md5.Sum([]byte("fixture-password" + r.Form.Get("s")))
		if r.Form.Get("u") != "sync-fixture" || r.Form.Get("t") != hex.EncodeToString(digest[:]) {
			t.Error("Navidrome publication did not use the environment account")
		}
		track := map[string]any{"id": "song-1", "title": "曲名", "artist": "歌手", "album": "专辑", "path": library.path}
		response := map[string]any{"status": "ok", "version": "1.16.1"}
		switch r.URL.Path {
		case "/rest/search3.view":
			library.searches++
			response["searchResult3"] = map[string]any{"song": []any{track}}
		case "/rest/getSong.view":
			response["song"] = track
		case "/rest/getPlaylists.view":
			playlists := []any{}
			if library.name != "" {
				playlists = append(playlists, map[string]any{"id": "playlist-fixture", "name": library.name})
			}
			response["playlists"] = map[string]any{"playlist": playlists}
		case "/rest/getPlaylist.view":
			entries := []any{}
			for _, id := range library.ids {
				entries = append(entries, map[string]any{"id": id})
			}
			response["playlist"] = map[string]any{"entry": entries}
		case "/rest/createPlaylist.view":
			library.creates++
			library.name, library.ids = r.Form.Get("name"), append([]string{}, r.Form["songId"]...)
			response["playlist"] = map[string]any{"id": "playlist-fixture"}
		case "/rest/updatePlaylist.view":
			library.updates++
			if r.Form.Get("playlistId") != "playlist-fixture" {
				t.Error("Navidrome playlist identity changed")
			}
			if r.Form.Get("name") != "" {
				library.name = r.Form.Get("name")
			}
			library.ids = append([]string{}, r.Form["songIdToAdd"]...)
		default:
			t.Errorf("unexpected Navidrome endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": response})
	}))
	t.Cleanup(server.Close)
	return config.Cloud115{
		NavidromeEnabled: true, NavidromePlaylistSync: true, MediaEnabled: true,
		NavidromeURL: server.URL, NavidromeMountRoot: "/library", NavidromeUsername: "sync-fixture",
		NavidromePasswordEnv: "MELOPORT_NAVIDROME_FIXTURE_PASSWORD", Timeout: config.Duration{Duration: 15 * time.Second},
	}, library
}

func TestNavidromePublisherSupportsLocalAndCloudPlaylistsAndRankings(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		for _, source := range []string{"", "playlist", "ranking"} {
			t.Run(fmt.Sprintf("cloud=%t/source=%s", cloud, source), func(t *testing.T) {
				root := t.TempDir()
				task := model.Task{ID: "favorites", Platform: "netease", SourceType: source, MusicRoot: filepath.Join(root, "music"), Directory: filepath.Join(root, "music", "favorites")}
				item := state.Download{TaskID: task.ID, Provider: task.Platform, TrackID: "1", Path: filepath.Join(task.Directory, "netease-1.flac"), Size: 32}
				relative := "favorites/netease-1.flac"
				if cloud {
					// A saved upload path remains authoritative even if the title
					// changed or the upload used the old task-only folder layout.
					relative = "old-cloud-folder/netease-1.flac"
				}
				cfg, library := navidromeLibrary(t, "/library/"+relative)
				cfg.Enabled, cfg.TargetCID = cloud, "12345"
				var store *state.Store
				if cloud {
					var err error
					store, err = state.Open(filepath.Join(root, "state.db"))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = store.Close() })
					_, err = store.SaveCloudIntent(t.Context(), state.CloudFile{
						RootCID: cfg.TargetCID, TaskID: task.ID, Provider: item.Provider, TrackID: item.TrackID,
						LocalPath: item.Path, StagingPath: filepath.Join(root, "staging", filepath.FromSlash(relative)),
						RelativePath: relative, Size: item.Size, SHA1: strings.Repeat("a", 40), MediaID: "fixture-media-id",
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				publisher := newNavidromePublisher(cfg, store)
				track := model.Track{ID: "1", Name: "曲名", Artists: []string{"歌手"}, Album: "专辑"}
				playlist := model.Playlist{Name: "收藏", Tracks: []model.Track{track, track}}
				for i := 0; i < 2; i++ {
					if err := publisher.PublishPlaylist(t.Context(), task, playlist, []state.Download{item}); err != nil {
						t.Fatal(err)
					}
				}
				if library.name != "网易云音乐 - 收藏" || library.creates != 1 || library.updates != 1 || library.searches != 2 || strings.Join(library.ids, ",") != "song-1" {
					t.Fatal("playlist name, repeated-song deduplication or idempotent update is incorrect")
				}
			})
		}
	}
}

func TestNavidromePublisherRejectsAudioOutsideMappedLocalRoot(t *testing.T) {
	root := t.TempDir()
	cfg, library := navidromeLibrary(t, "/library/favorites/netease-1.flac")
	item := state.Download{TaskID: "favorites", Provider: "netease", TrackID: "1", Path: filepath.Join(root, "outside", "netease-1.flac")}
	task := model.Task{ID: item.TaskID, Platform: item.Provider, SourceType: "playlist", MusicRoot: filepath.Join(root, "music")}
	err := newNavidromePublisher(cfg, nil).PublishPlaylist(t.Context(), task, model.Playlist{Name: "收藏", Tracks: []model.Track{{ID: "1", Name: "曲名", Artists: []string{"歌手"}}}}, []state.Download{item})
	if err == nil || library.creates != 0 || library.updates != 0 || library.searches != 0 {
		t.Fatal("an unmapped local file was published to Navidrome")
	}
}

type navidromeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f navidromeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCLILocalNavidromeSyncUsesExistingAudioWithout115OrChkszKey(t *testing.T) {
	cloud, library := navidromeLibrary(t, "/library/test/netease-1.flac")
	path := cliConfig(t, "metadata:\n  fallback_to_api: false\n  netease_cookie_env: MELOPORT_NAVIDROME_CLI_COOKIE\napi:\n  key_env: MELOPORT_NAVIDROME_CLI_KEY\ncloud115:\n  enabled: false\n  media_enabled: true\n  navidrome_enabled: true\n  navidrome_playlist_sync: true\n  navidrome_url: "+cloud.NavidromeURL+"\n  navidrome_mount_root: /library\n  navidrome_username: sync-fixture\n  navidrome_password_env: MELOPORT_NAVIDROME_FIXTURE_PASSWORD\n")
	t.Setenv("MELOPORT_NAVIDROME_CLI_KEY", "")
	t.Setenv("MELOPORT_NAVIDROME_CLI_COOKIE", "")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cfg.Tasks[0].Directory, "netease-1.flac")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile(filepath.Join("..", "download", "testdata", "tone.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, audio, 0600); err != nil {
		t.Fatal(err)
	}
	if err := taglib.WriteTags(file, map[string][]string{"TITLE": {"曲名"}, "ARTIST": {"歌手"}, "ALBUM": {"专辑"}, "LYRICS": {"[00:00.00]fixture"}}, 0); err != nil {
		t.Fatal(err)
	}
	audio, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(cfg.Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	item := state.Download{TaskID: "test", Provider: "netease", TrackID: "1", Path: file, Size: int64(len(audio)), SHA256: fmt.Sprintf("%x", sha256.Sum256(audio)), Title: "曲名", Artists: []string{"歌手"}, Album: "专辑"}
	if err := store.SaveDownload(t.Context(), item); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	nativeCalls := 0
	http.DefaultTransport = navidromeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "music.163.com" && r.URL.Path == "/api/linux/forward" {
			nativeCalls++
			body := `{"code":200,"playlist":{"id":123,"name":"收藏","trackCount":2,"trackIds":[{"id":1},{"id":1}],"tracks":[{"id":1,"name":"曲名","ar":[{"name":"歌手"}],"al":{"name":"专辑"}}]}}`
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		if r.URL.Hostname() != "127.0.0.1" {
			t.Errorf("unexpected upstream during a local skipped sync: %s", r.URL.Hostname())
			return nil, fmt.Errorf("unexpected fixture request")
		}
		return previous.RoundTrip(r)
	})
	for i := 0; i < 2; i++ {
		var out, diagnostic bytes.Buffer
		if err := Run(context.Background(), []string{"sync", "-config", path}, &out, &diagnostic, "test"); err != nil {
			t.Fatal(err)
		}
		var summary syncer.Summary
		if err := json.Unmarshal(out.Bytes(), &summary); err != nil || summary.Status != "success" || summary.Skipped != 1 || summary.Downloaded != 0 || summary.PlaylistWarning != "" {
			t.Fatalf("local playlist publication did not finish without audio downloads: %+v, %v", summary, err)
		}
	}
	if library.creates != 1 || library.updates != 1 || library.name != "网易云音乐 - 收藏" || nativeCalls != 2 {
		t.Fatal("local CLI publication did not create once and update the same playlist")
	}
}
