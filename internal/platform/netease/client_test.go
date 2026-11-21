package netease

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meloport/internal/platform"
)

func fixtureClient(server *httptest.Server) *Client {
	return New(Options{BaseURL: server.URL, HTTP: platform.NewHTTP(server.Client(), time.Second, 0), Cookie: "MUSIC_U=platform_cookie_only"})
}

func TestParseTrackKeepsNativeAlbumTags(t *testing.T) {
	item := decodeJSON(t, []byte(`{"id":123,"name":"歌曲","ar":[{"name":"主唱"}],"al":{"name":"专辑","picUrl":"http://p2.music.126.net/album.jpg","artists":[{"name":"专辑艺人"}]},"no":7,"cd":"2","publishTime":1704038400000}`))
	track, err := parseTrack(item)
	if err != nil || track.CoverURL != "https://p2.music.126.net/album.jpg" || track.TrackNumber != 7 || track.DiscNumber != 2 || track.Year != "2024" || !reflect.DeepEqual(track.AlbumArtists, []string{"专辑艺人"}) {
		t.Fatalf("unexpected native NetEase album tags: %+v, %v", track, err)
	}
	track, err = parseTrack(decodeJSON(t, []byte(`{"id":123,"al":{"picUrl":"https://p2.music.126.net/album.jpg?token=nonpublic"},"no":0,"cd":"A","publishTime":0}`)))
	if err != nil || track.CoverURL != "" || track.TrackNumber != 0 || track.DiscNumber != 0 || track.Year != "" || len(track.AlbumArtists) > 0 {
		t.Fatalf("invented NetEase metadata or retained a signed cover: %+v, %v", track, err)
	}
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func decryptECB(t *testing.T, encoded, key string) []byte {
	t.Helper()
	data, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		t.Fatal("protocol key length invalid")
	}
	if len(data) == 0 || len(data)%block.BlockSize() != 0 {
		t.Fatal("invalid encrypted block length")
	}
	result := make([]byte, len(data))
	for i := 0; i < len(data); i += block.BlockSize() {
		block.Decrypt(result[i:i+block.BlockSize()], data[i:i+block.BlockSize()])
	}
	return unpad(t, result)
}

func decryptCBC(t *testing.T, encoded string, key []byte) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || len(data)%block.BlockSize() != 0 {
		t.Fatal("invalid encrypted block length")
	}
	cipher.NewCBCDecrypter(block, []byte(weapiIV)).CryptBlocks(data, data)
	return unpad(t, data)
}

func unpad(t *testing.T, data []byte) []byte {
	t.Helper()
	n := int(data[len(data)-1])
	if n < 1 || n > aes.BlockSize || n > len(data) {
		t.Fatal("invalid protocol padding")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			t.Fatal("invalid protocol padding bytes")
		}
	}
	return data[:len(data)-n]
}

func TestProtocolPayloads(t *testing.T) {
	params := map[string]any{"id": "123", "n": 100000, "s": 8}
	linux, err := linuxForm("/api/v3/playlist/detail", params)
	if err != nil {
		t.Fatal(err)
	}
	form, err := url.ParseQuery(string(linux))
	if err != nil {
		t.Fatal(err)
	}
	message := decodeJSON(t, decryptECB(t, form.Get("eparams"), linuxKey))
	if text(message, "method") != "POST" || text(message, "url") != "https://music.163.com/api/v3/playlist/detail" {
		t.Fatal("incorrect forward target", message)
	}
	if p := message["params"].(map[string]any); text(p, "id") != "123" || text(p, "n") != "100000" {
		t.Fatal("incorrect encrypted params", p)
	}

	secret := []byte("abcdefghijklmnop")
	weapi, err := weapiFormWithSecret(params, secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "abcdefghijklmnop" {
		t.Fatal("nonce was mutated")
	}
	form, _ = url.ParseQuery(string(weapi))
	inner := decryptCBC(t, form.Get("params"), secret)
	message = decodeJSON(t, decryptCBC(t, string(inner), []byte(weapiKey)))
	if text(message, "id") != "123" {
		t.Fatal("incorrect weapi payload")
	}
	if len(form.Get("encSecKey")) != 256 {
		t.Fatal("wrong RSA block size")
	}
	if _, err := hex.DecodeString(form.Get("encSecKey")); err != nil {
		t.Fatal("invalid RSA encoding")
	}
	if _, err := weapiFormWithSecret(params, secret[:10]); err == nil {
		t.Fatal("accepted invalid nonce")
	}

	eapi, err := eapiForm("/api/search/song/list/page", params)
	if err != nil {
		t.Fatal(err)
	}
	form, _ = url.ParseQuery(string(eapi))
	parts := strings.Split(string(decryptECB(t, form.Get("params"), eapiKey)), "-36cd479b6b5-")
	if len(parts) != 3 || parts[0] != "/api/search/song/list/page" {
		t.Fatal("wrong eapi envelope")
	}
	expected := md5.Sum([]byte("nobody" + parts[0] + "use" + parts[1] + "md5forencrypt"))
	if parts[2] != hex.EncodeToString(expected[:]) {
		t.Fatal("wrong eapi digest")
	}
}

func TestPlaylistNativeRequestAndMetadata(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/linux/forward" || r.URL.RawQuery != "" {
			t.Errorf("unexpected native request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad request", 400)
			return
		}
		if r.Header.Get("Cookie") != "MUSIC_U=platform_cookie_only" || r.Header.Get("Authorization") != "" {
			t.Error("wrong credential isolation")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if r.Form.Get("eparams") == "" || r.Form.Get("apikey") != "" {
			t.Error("wrong form fields")
		}
		io.WriteString(w, `{"code":200,"playlist":{"id":123,"name":"My list","trackCount":2,"trackIds":[{"id":1},{"id":"9007199254740993"}],"tracks":[{"id":1,"name":"First","ar":[{"name":"Artist"}],"al":{"name":"Album"},"dt":123000},{"id":"9007199254740993","pc":{"sn":"Cloud","ar":"Cloud singer","alb":"Cloud album"}}]}}`)
	}))
	defer server.Close()
	list, err := fixtureClient(server).Playlist(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if list.Name != "My list" || list.ReportedCount != 2 || len(list.Tracks) != 2 {
		t.Fatalf("wrong list: %+v", list)
	}
	if list.Tracks[0].Name != "First" || list.Tracks[0].Duration != 123 || list.Tracks[0].Album != "Album" || !reflect.DeepEqual(list.Tracks[0].Artists, []string{"Artist"}) {
		t.Fatalf("wrong metadata %+v", list.Tracks[0])
	}
	if list.Tracks[1].ID != "9007199254740993" || list.Tracks[1].Name != "Cloud" {
		t.Fatalf("wrong cloud metadata %+v", list.Tracks[1])
	}
	if requests.Load() != 1 {
		t.Fatal("unnecessary extra metadata requests")
	}
}

func TestRankingsCatalogUsesNativeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/toplist" || r.Header.Get("Cookie") != "MUSIC_U=platform_cookie_only" {
			t.Errorf("unexpected ranking request: %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"code":200,"list":[{"id":26,"name":"热歌榜","trackCount":100,"updateFrequency":null,"updateFrequency":"每天"}]}`)
	}))
	defer server.Close()
	items, err := fixtureClient(server).Rankings(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != "26" || items[0].Name != "热歌榜" || items[0].TrackCount != 100 {
		t.Fatalf("unexpected rankings: %+v %v", items, err)
	}
}

func TestPlaylistMissingDetailsFixedBatchesAndOrder(t *testing.T) {
	var detailCalls atomic.Int32
	const total = 1002
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/linux/forward" {
			ids := make([]map[string]int, total)
			for i := range ids {
				ids[i] = map[string]int{"id": i + 1}
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "playlist": map[string]any{"id": 123, "trackCount": total, "trackIds": ids, "tracks": []map[string]any{{"id": 1, "name": "embedded"}}}})
			return
		}
		if r.URL.Path != "/weapi/v3/song/detail" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.Error(w, "bad endpoint", 404)
			return
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("params") == "" || len(r.Form.Get("encSecKey")) != 256 {
			t.Error("missing encrypted detail request")
		}
		call := detailCalls.Add(1)
		start, end := 2, 1001
		if call == 2 {
			start, end = 1002, 1002
		}
		if call > 2 {
			t.Error("unbounded detail requests")
			http.Error(w, "too many", 500)
			return
		}
		songs := []map[string]any{}
		// Reverse each returned batch to prove output follows member IDs, not HTTP order.
		for id := end; id >= start; id-- {
			songs = append(songs, map[string]any{"id": id, "name": strconv.Itoa(id)})
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "songs": songs})
	}))
	defer server.Close()
	list, err := fixtureClient(server).Playlist(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tracks) != total || detailCalls.Load() != 2 {
		t.Fatalf("wrong batching %d %d", len(list.Tracks), detailCalls.Load())
	}
	for i, track := range list.Tracks {
		if track.ID != strconv.Itoa(i+1) {
			t.Fatalf("wrong membership order at %d", i)
		}
	}
}

func TestPlaylistDuplicateMembersAndEmpty(t *testing.T) {
	for name, response := range map[string]string{
		"empty":     `{"code":200,"playlist":{"id":123,"trackCount":0,"trackIds":[],"tracks":[]}}`,
		"duplicate": `{"code":200,"playlist":{"id":123,"trackCount":2,"trackIds":[{"id":1},{"id":1}],"tracks":[{"id":1}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, response) }))
			defer server.Close()
			list, err := fixtureClient(server).Playlist(context.Background(), "123")
			if err != nil {
				t.Fatal(err)
			}
			if name == "empty" && (list.ReportedCount != 0 || list.Tracks == nil || len(list.Tracks) != 0) {
				t.Fatal("empty playlist not preserved")
			}
			if name == "duplicate" && len(list.Tracks) != 2 {
				t.Fatal("original membership count not preserved")
			}
		})
	}
}

func TestRejectMalformedOrIncompletePlaylist(t *testing.T) {
	cases := map[string]string{
		"business error": `{"code":301,"msg":"MUSIC_U=never_echo_this"}`,
		"no status":      `{"playlist":{}}`,
		"no playlist":    `{"code":200}`,
		"wrong playlist": `{"code":200,"playlist":{"id":456,"trackIds":[]}}`,
		"no members":     `{"code":200,"playlist":{"tracks":[]}}`,
		"bad count":      `{"code":200,"playlist":{"trackCount":2,"trackIds":[{"id":1}]}}`,
		"bad member":     `{"code":200,"playlist":{"trackIds":[1]}}`,
		"zero member":    `{"code":200,"playlist":{"trackIds":[{"id":0}]}}`,
		"bad tracks":     `{"code":200,"playlist":{"trackIds":[],"tracks":{}}}`,
		"bad track":      `{"code":200,"playlist":{"trackIds":[{"id":1}],"tracks":[{"name":"missing id"}]}}`,
		"wrong track":    `{"code":200,"playlist":{"trackIds":[{"id":1}],"tracks":[{"id":2}]}}`,
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, response) }))
			defer server.Close()
			_, err := fixtureClient(server).Playlist(context.Background(), "123")
			if err == nil {
				t.Fatal("accepted malformed playlist")
			}
			if strings.Contains(err.Error(), "never_echo_this") {
				t.Fatal("raw upstream message leaked")
			}
		})
	}
}

func TestRejectIncompleteDetailBatch(t *testing.T) {
	for _, detail := range []string{`{"code":200,"songs":[]}`, `{"code":200,"songs":[{"id":2}]}`, `{"code":200}`, `{"code":301}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/linux/forward" {
				io.WriteString(w, `{"code":200,"playlist":{"trackIds":[{"id":1}],"tracks":[]}}`)
			} else {
				io.WriteString(w, detail)
			}
		}))
		_, err := fixtureClient(server).Playlist(context.Background(), "123")
		server.Close()
		if err == nil {
			t.Fatal("accepted incomplete details", detail)
		}
	}
}

func TestNativeSearchProtocolAndResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eapi/batch" {
			t.Error("wrong search endpoint")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		parts := strings.Split(string(decryptECB(t, r.Form.Get("params"), eapiKey)), "-36cd479b6b5-")
		if len(parts) != 3 || parts[0] != "/api/search/song/list/page" {
			t.Error("wrong search target")
			return
		}
		params := decodeJSON(t, []byte(parts[1]))
		if text(params, "keyword") != "歌曲 Singer" || text(params, "offset") != "20" || text(params, "limit") != "20" || params["total"] != false {
			t.Error("wrong search params", params)
		}
		io.WriteString(w, `{"code":200,"data":{"totalCount":22,"resources":[{"baseInfo":{"simpleSongData":{"id":21,"name":"Song","ar":[{"name":"Singer"}],"al":{"name":"Record"},"dt":123456}}},{"baseInfo":{"simpleSongData":{"id":21,"name":"duplicate"}}}]}}`)
	}))
	defer server.Close()
	tracks, err := fixtureClient(server).Search(context.Background(), "歌曲 Singer", 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].ID != "21" || tracks[0].Name != "Song" || tracks[0].Duration != 123 {
		t.Fatalf("wrong search result %+v", tracks)
	}
}

func TestNativeSearchEmptyAndMalformed(t *testing.T) {
	for name, response := range map[string]string{
		"empty":             `{"code":200,"data":{"totalCount":0,"resources":[]}}`,
		"empty null":        `{"code":200,"data":{"totalCount":0,"resources":null}}`,
		"no data":           `{"code":200}`,
		"missing resources": `{"code":200,"data":{}}`,
		"incomplete":        `{"code":200,"data":{"totalCount":20,"resources":[]}}`,
		"bad resource":      `{"code":200,"data":{"resources":[1]}}`,
		"bad metadata":      `{"code":200,"data":{"resources":[{}]}}`,
		"bad song":          `{"code":200,"data":{"resources":[{"baseInfo":{"simpleSongData":{"name":"No id"}}}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, response) }))
			defer server.Close()
			tracks, err := fixtureClient(server).Search(context.Background(), "query", 20, 0)
			if strings.HasPrefix(name, "empty") {
				if err != nil || tracks == nil || len(tracks) != 0 {
					t.Fatalf("empty search failed %v", err)
				}
			} else if err == nil {
				t.Fatal("accepted malformed search")
			}
		})
	}
}

func TestInvalidInputsAndCancellation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "do not reach", 500) }))
	defer server.Close()
	client := fixtureClient(server)
	for _, id := range []string{"", "0", "01", "../1", "1e3", strings.Repeat("1", 21)} {
		if _, err := client.Playlist(context.Background(), id); err == nil {
			t.Fatal("accepted invalid id")
		}
	}
	for _, args := range []struct {
		keyword       string
		limit, offset int
	}{{"", 20, 0}, {"x", 0, 0}, {"x", 101, 0}, {"x", 20, -1}, {"x", 20, 100001}} {
		if _, err := client.Search(context.Background(), args.keyword, args.limit, args.offset); err == nil {
			t.Fatal("accepted invalid search")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Playlist(ctx, "123"); err == nil {
		t.Fatal("ignored cancellation")
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid/canceled calls sent HTTP: %d", requests.Load())
	}
}

func TestNativeNetworkFailureDoesNotLeakCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "MUSIC_U=platform_cookie_only", http.StatusForbidden)
	}))
	defer server.Close()
	_, err := fixtureClient(server).Playlist(context.Background(), "123")
	if err == nil || strings.Contains(err.Error(), "platform_cookie_only") {
		t.Fatalf("unsafe error %v", err)
	}
}

func TestRankingsCatalogAndRankingReuseNativePlaylist(t *testing.T) {
	var catalog, detail int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/toplist":
			catalog++
			io.WriteString(w, `{"code":200,"list":[{"id":19723756,"name":"飙升榜","trackCount":100,"updateFrequency":"每小时"}]}`)
		case "/api/linux/forward":
			detail++
			io.WriteString(w, `{"code":200,"playlist":{"id":19723756,"name":"飙升榜","trackCount":1,"trackIds":[{"id":123}],"tracks":[{"id":123,"name":"Song","ar":[{"name":"Artist"}],"al":{"name":"Album"},"dt":180000}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := fixtureClient(server)
	catalogue, err := client.Rankings(context.Background())
	if err != nil || len(catalogue) != 1 || catalogue[0].ID != "19723756" || catalogue[0].TrackCount != 100 || catalogue[0].UpdateFrequency != "每小时" {
		t.Fatalf("bad NetEase ranking catalog: %+v %v", catalogue, err)
	}
	list, err := client.Ranking(context.Background(), "19723756")
	if err != nil || list.ID != "19723756" || len(list.Tracks) != 1 || list.Tracks[0].ID != "123" {
		t.Fatalf("bad NetEase ranking detail: %+v %v", list, err)
	}
	if catalog != 1 || detail != 1 {
		t.Fatalf("unexpected native calls catalog=%d detail=%d", catalog, detail)
	}
}

func TestRankingsRejectInvalidCatalogEntries(t *testing.T) {
	for _, response := range []string{
		`{"code":200}`,
		`{"code":200,"list":[1]}`,
		`{"code":200,"list":[{"id":0,"name":"Bad"}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, response) }))
		_, err := fixtureClient(server).Rankings(context.Background())
		server.Close()
		if err == nil {
			t.Fatalf("accepted malformed ranking catalog: %s", response)
		}
	}
}

func TestSafetyCap(t *testing.T) {
	ids := make([]map[string]int, maxPlaylistTracks+1)
	for i := range ids {
		ids[i] = map[string]int{"id": i + 1}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "playlist": map[string]any{"trackIds": ids}})
	}))
	defer server.Close()
	if _, err := fixtureClient(server).Playlist(context.Background(), "123"); err == nil {
		t.Fatal("ignored member safety cap")
	}
}

func ExampleClient() {
	// Production uses NetEase metadata endpoints. ChKSz credentials are never
	// passed to this client; fixtures pass an explicit local BaseURL for tests.
	client := New(Options{})
	fmt.Println(client != nil)
	// Output: true
}
