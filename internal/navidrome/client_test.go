package navidrome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPublishCreatesPlaylistAfterExactMountedPathMatches(t *testing.T) {
	var creates int
	var added []string
	var searchQueries int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form := readAuthedForm(t, r)
		switch r.URL.Path {
		case "/rest/search3.view":
			searchQueries++
			if form.Get("query") != "曲名 歌手甲" || form.Get("songCount") != "50" {
				t.Errorf("wrong search: %v", form)
			}
			writeResponse(w, map[string]any{"searchResult3": map[string]any{"song": []any{
				map[string]any{"id": "wrong-folder", "title": "曲名", "artist": "歌手甲", "album": "专辑", "path": "/music/115/qq/榜单/qq-42.flac"},
				map[string]any{"id": "wrong-version", "title": "曲名", "artist": "歌手甲", "album": "其他专辑", "path": "/music/115/netease/榜单/netease-42.flac"},
				map[string]any{"id": "song-42", "title": "曲名", "artist": "歌手甲", "album": "专辑", "path": "/music/115/netease/榜单/netease-42.flac"},
			}}})
		case "/rest/getSong.view":
			if form.Get("id") != "song-42" {
				t.Errorf("unexpected song detail ID: %s", form.Get("id"))
			}
			writeResponse(w, map[string]any{"song": map[string]any{"id": "song-42", "title": "曲名", "artist": "歌手甲", "album": "专辑", "path": "/music/115/netease/榜单/netease-42.flac"}})
		case "/rest/getPlaylists.view":
			writeResponse(w, map[string]any{"playlists": map[string]any{}})
		case "/rest/createPlaylist.view":
			creates++
			if form.Get("name") != "[MeloPort] 网易云 - 榜单" {
				t.Errorf("wrong name: %s", form.Get("name"))
			}
			added = form["songId"]
			writeResponse(w, map[string]any{"playlist": map[string]any{"id": "new-id"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := mustClientAt(t, server.URL, "alice", "secret", "/music/115")
	if err := client.Publish(context.Background(), "[MeloPort] 网易云 - 榜单", []Candidate{{Title: "曲名", Artists: []string{"歌手甲"}, Album: "专辑", RelativePath: "netease/榜单/netease-42.flac"}}); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || searchQueries != 1 || len(added) != 1 || added[0] != "song-42" {
		t.Fatalf("wrong publication: creates=%d searches=%d added=%v", creates, searchQueries, added)
	}
}

func TestPublishReplacesExistingPlaylistContents(t *testing.T) {
	var remove []string
	var added []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form := readAuthedForm(t, r)
		switch r.URL.Path {
		case "/rest/search3.view":
			writeResponse(w, map[string]any{"searchResult3": map[string]any{"song": map[string]any{"id": "new-song", "title": "新歌", "artist": "歌手乙", "album": "新专辑", "path": "/music/netease/榜单/netease-2.flac"}}})
		case "/rest/getSong.view":
			writeResponse(w, map[string]any{"song": map[string]any{"id": "new-song", "title": "新歌", "artist": "歌手乙", "album": "新专辑", "path": "/music/netease/榜单/netease-2.flac"}})
		case "/rest/getPlaylists.view":
			writeResponse(w, map[string]any{"playlists": map[string]any{"playlist": map[string]any{"id": "playlist-id", "name": "榜单"}}})
		case "/rest/getPlaylist.view":
			if form.Get("id") != "playlist-id" {
				t.Errorf("unexpected playlist ID: %s", form.Get("id"))
			}
			writeResponse(w, map[string]any{"playlist": map[string]any{"entry": []any{map[string]any{"id": "old-1"}, map[string]any{"id": "old-2"}}}})
		case "/rest/updatePlaylist.view":
			if form.Get("playlistId") != "playlist-id" {
				t.Errorf("unexpected update target: %v", form)
			}
			remove, added = form["songIndexToRemove"], form["songIdToAdd"]
			writeResponse(w, map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := mustClient(t, server.URL, "alice", "secret")
	if err := client.Publish(context.Background(), "榜单", []Candidate{{Title: "新歌", Artists: []string{"歌手乙"}, Album: "新专辑", RelativePath: "netease/榜单/netease-2.flac"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(remove, ",") != "0,1" || strings.Join(added, ",") != "new-song" {
		t.Fatalf("existing playlist not replaced: remove=%v add=%v", remove, added)
	}
}

func TestPublishRenamesLegacyPlaylistWithoutCreatingAnother(t *testing.T) {
	const name, legacyName = "网易云音乐 - 榜单", "网易云音乐 - 榜单 [daily]"
	storedName := legacyName
	var creates, updates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form := readAuthedForm(t, r)
		switch r.URL.Path {
		case "/rest/search3.view":
			writeResponse(w, map[string]any{"searchResult3": map[string]any{"song": map[string]any{"id": "song", "title": "歌曲", "artist": "歌手", "path": "/music/daily/netease-1.flac"}}})
		case "/rest/getSong.view":
			writeResponse(w, map[string]any{"song": map[string]any{"id": "song", "title": "歌曲", "artist": "歌手", "path": "/music/daily/netease-1.flac"}})
		case "/rest/getPlaylists.view":
			writeResponse(w, map[string]any{"playlists": map[string]any{"playlist": map[string]any{"id": "keep-this-id", "name": storedName}}})
		case "/rest/getPlaylist.view":
			writeResponse(w, map[string]any{"playlist": map[string]any{"entry": []any{map[string]any{"id": "song"}}}})
		case "/rest/createPlaylist.view":
			creates++
			writeResponse(w, map[string]any{})
		case "/rest/updatePlaylist.view":
			updates++
			if form.Get("playlistId") != "keep-this-id" || strings.Join(form["songIdToAdd"], ",") != "song" {
				t.Error("legacy rename changed the playlist identity or duplicated a song")
			}
			if updates == 1 && form.Get("name") != name {
				t.Error("legacy playlist was not renamed")
			}
			if form.Get("name") != "" {
				storedName = form.Get("name")
			}
			writeResponse(w, map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := mustClient(t, server.URL, "alice", "secret")
	tracks := []Candidate{{Title: "歌曲", Artists: []string{"歌手"}, RelativePath: "daily/netease-1.flac"}}
	for i := 0; i < 2; i++ {
		if err := client.Publish(context.Background(), name, tracks, legacyName); err != nil {
			t.Fatal(err)
		}
	}
	if creates != 0 || updates != 2 || storedName != name {
		t.Fatalf("legacy migration created a duplicate or lost its name: creates=%d updates=%d", creates, updates)
	}
}

func TestPublishDoesNotMutateWhenAnyTrackIsNotIndexed(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readAuthedForm(t, r)
		switch r.URL.Path {
		case "/rest/search3.view":
			writeResponse(w, map[string]any{"searchResult3": map[string]any{"song": map[string]any{"id": "same-title-wrong-path", "title": "好歌", "artist": "歌手丙", "album": "专辑", "path": "/music/other/netease-1.flac"}}})
		case "/rest/getPlaylists.view", "/rest/createPlaylist.view", "/rest/updatePlaylist.view":
			if strings.Contains(r.URL.Path, "createPlaylist") || strings.Contains(r.URL.Path, "updatePlaylist") {
				mutations++
			}
			writeResponse(w, map[string]any{"playlists": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := mustClient(t, server.URL, "alice", "secret")
	err := client.Publish(context.Background(), "榜单", []Candidate{{Title: "好歌", Artists: []string{"歌手丙"}, Album: "专辑", RelativePath: "netease/榜单/netease-1.flac"}})
	if err == nil || !strings.Contains(err.Error(), "not indexed") || mutations != 0 {
		t.Fatalf("wrong missing-track result: err=%v mutations=%d", err, mutations)
	}
}

func TestPublishRejectsAmbiguousSearchResultsAndDuplicatePlaylistNames(t *testing.T) {
	var duplicate bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form := readAuthedForm(t, r)
		switch r.URL.Path {
		case "/rest/search3.view":
			writeResponse(w, map[string]any{"searchResult3": map[string]any{"song": []any{
				map[string]any{"id": "one", "title": "歌", "artist": "艺人", "album": "", "path": "/music/netease/a/netease-1.flac"},
				map[string]any{"id": "two", "title": "歌", "artist": "艺人", "album": "", "path": "/music/netease/a/netease-1.flac"},
			}}})
		case "/rest/getSong.view":
			id := form.Get("id")
			writeResponse(w, map[string]any{"song": map[string]any{"id": id, "title": "歌", "artist": "艺人", "path": "/music/netease/a/netease-1.flac"}})
		case "/rest/getPlaylists.view":
			if duplicate {
				writeResponse(w, map[string]any{"playlists": map[string]any{"playlist": []any{map[string]any{"id": "1", "name": "歌单"}, map[string]any{"id": "2", "name": "歌单"}}}})
			} else {
				writeResponse(w, map[string]any{"playlists": map[string]any{}})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := mustClient(t, server.URL, "alice", "secret")
	candidate := Candidate{Title: "歌", Artists: []string{"艺人"}, RelativePath: "netease/a/netease-1.flac"}
	if err := client.Publish(context.Background(), "歌单", []Candidate{candidate}); err == nil || !strings.Contains(err.Error(), "multiple Navidrome songs") {
		t.Fatalf("ambiguous matches accepted: %v", err)
	}
	duplicate = true
	if err := client.Publish(context.Background(), "歌单", nil); err == nil || !strings.Contains(err.Error(), "multiple playlists") {
		t.Fatalf("duplicate playlist names accepted: %v", err)
	}
}

func TestPublishValidatesBeforeRequestAndDoesNotLeakCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "secret") {
			t.Errorf("plain password leaked in request: %q", body)
		}
		if r.URL.Path == "/rest/createPlaylist.view" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeResponse(w, map[string]any{"error": map[string]any{"code": 40, "message": "invalid token"}})
	}))
	defer server.Close()
	client := mustClient(t, server.URL, "alice", "secret")
	if err := client.Publish(context.Background(), "bad", []Candidate{{Title: "bad", RelativePath: "../outside.mp3"}}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe path accepted or error leaked credentials: %v", err)
	}
	if err := client.Publish(context.Background(), "safe", nil); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Navidrome failure was not safely reported: %v", err)
	}
}

func TestNewRejectsCredentialsInURLAndUnsafeMount(t *testing.T) {
	for _, opts := range []Options{
		{BaseURL: "http://alice:secret@localhost:4533", Username: "alice", Password: "secret", MountRoot: "/music"},
		{BaseURL: "http://localhost:4533", Username: "alice", Password: "secret", MountRoot: "/music/../other"},
	} {
		if _, err := New(opts); err == nil {
			t.Fatalf("accepted invalid options: %+v", opts)
		}
	}
}

func mustClient(t *testing.T, base, username, password string) *Client {
	return mustClientAt(t, base, username, password, "/music")
}

func mustClientAt(t *testing.T, base, username, password, mountRoot string) *Client {
	t.Helper()
	client, err := New(Options{BaseURL: base, Username: username, Password: password, MountRoot: mountRoot, HTTPClient: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func readAuthedForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Query().Get("u") != "" || !strings.HasSuffix(r.URL.Path, ".view") {
		t.Errorf("credentials must be sent in POST body: %s %s", r.Method, r.URL.String())
	}
	if err := r.ParseForm(); err != nil {
		t.Errorf("parse request: %v", err)
	}
	salt := r.Form.Get("s")
	digest := md5.Sum([]byte("secret" + salt))
	if r.Form.Get("u") != "alice" || r.Form.Get("t") != hex.EncodeToString(digest[:]) || r.Form.Get("v") != "1.16.1" || r.Form.Get("f") != "json" {
		t.Errorf("invalid Subsonic token auth: %v", r.Form)
	}
	return r.Form
}

func writeResponse(w http.ResponseWriter, value map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	value["status"] = "ok"
	value["version"] = "1.16.1"
	_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": value})
}
