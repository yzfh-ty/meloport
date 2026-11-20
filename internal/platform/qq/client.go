// Package qq reads QQ Music metadata directly from its platform endpoints.
// It does not resolve audio URLs or send ChKSz credentials.
package qq

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"meloport/internal/model"
	"meloport/internal/platform"
)

const (
	playlistPageSize = 100
	maxPlaylistSize  = 50000
	searchPageSize   = 60
)

var (
	numericID    = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	midPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	pathPlaylist = regexp.MustCompile(`^/n/(?:yqq|ryqq)/playlist/([1-9][0-9]{0,19})(?:\.html)?/?$`)
)

type Options struct {
	HTTP    *platform.HTTP
	BaseURL string
	Cookie  string
}

type Client struct {
	http    *platform.HTTP
	baseURL string
	cookie  string
	initErr error
}

func New(opts Options) *Client {
	if opts.HTTP == nil {
		opts.HTTP = platform.NewHTTP(nil, 30*time.Second, time.Second)
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://u.y.qq.com"
	}
	c := &Client{http: opts.HTTP, baseURL: strings.TrimRight(opts.BaseURL, "/"), cookie: opts.Cookie}
	u, err := url.Parse(c.baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		c.initErr = errors.New("QQ platform base URL is invalid")
	}
	return c
}

// NormalizePlaylist accepts full official playlist URLs and numeric disstid.
// Share short links require expansion by the caller and are not fetched here.
func NormalizePlaylist(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if numericID.MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return "", playlistLinkError()
	}
	switch strings.ToLower(u.Hostname()) {
	case "y.qq.com", "i.y.qq.com", "c.y.qq.com":
	default:
		return "", playlistLinkError()
	}
	for key := range u.Query() {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "cookie") || strings.Contains(lower, "apikey") || strings.Contains(lower, "pass") || strings.Contains(lower, "credential") {
			return "", playlistLinkError()
		}
	}
	if matches := pathPlaylist.FindStringSubmatch(u.Path); len(matches) == 2 {
		return matches[1], nil
	}
	switch u.Path {
	case "/n2/m/share/details/taoge.html", "/n/ryqq/playlist", "/n/yqq/playlist", "/n/m/detail/taoge/index.html":
		values := u.Query()["id"]
		if len(values) == 1 && numericID.MatchString(values[0]) {
			return values[0], nil
		}
	}
	return "", playlistLinkError()
}

func playlistLinkError() error {
	return errors.New("QQ playlist must be a numeric playlist ID or a full official QQ Music playlist URL; expand short links first")
}

func (c *Client) Playlist(ctx context.Context, rawID string) (model.Playlist, error) {
	var empty model.Playlist
	if c.initErr != nil {
		return empty, c.initErr
	}
	id, err := NormalizePlaylist(rawID)
	if err != nil {
		return empty, err
	}
	result := model.Playlist{ID: id, Tracks: make([]model.Track, 0)}
	total := -1
	seenIDs := make(map[string]bool)
	for start := 0; ; start += playlistPageSize {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if start >= maxPlaylistSize {
			return empty, errors.New("QQ playlist exceeds the 50000-track safety limit")
		}
		payload := map[string]any{
			"comm": map[string]any{"cv": 4747474, "ct": 24, "format": "json", "inCharset": "utf-8", "outCharset": "utf-8", "platform": "yqq.json", "needNewCode": 1, "uin": 0},
			"req_1": map[string]any{
				"module": "music.srfDissInfo.aiDissInfo", "method": "uniform_get_Dissinfo",
				"param": map[string]any{"disstid": json.Number(id), "userinfo": 1, "tag": 1, "orderlist": 1, "song_begin": start, "song_num": playlistPageSize, "onlysonglist": 0, "enc_host_uin": ""},
			},
		}
		root, err := c.post(ctx, "/cgi-bin/musicu.fcg", payload, id, false)
		if err != nil {
			return empty, fmt.Errorf("read QQ playlist page: %w", err)
		}
		data, err := responseData(root, "req_1")
		if err != nil {
			return empty, err
		}
		pageTotal, ok := platform.Integer(data["total_song_num"])
		if !ok || pageTotal < 0 || pageTotal > maxPlaylistSize {
			return empty, errors.New("QQ playlist returned an invalid total")
		}
		if total < 0 {
			total = int(pageTotal)
			result.ReportedCount = total
			info := platform.Object(data["dirinfo"])
			result.Name = platform.Text(info, "title")
		} else if total != int(pageTotal) {
			return empty, errors.New("QQ playlist changed during pagination; retry the task later")
		}
		items, ok := data["songlist"].([]any)
		if !ok || len(items) > playlistPageSize {
			return empty, errors.New("QQ playlist returned an invalid song page")
		}
		if start+len(items) > total {
			return empty, errors.New("QQ playlist returned more tracks than its declared total")
		}
		if len(items) == 0 && start < total {
			return empty, errors.New("QQ playlist is incomplete: an expected page is empty")
		}
		for _, item := range items {
			track, err := decodeTrack(item)
			if err != nil {
				return empty, err
			}
			if seenIDs[track.ID] {
				return empty, errors.New("QQ playlist contains duplicate or repeated-page song IDs")
			}
			seenIDs[track.ID] = true
			result.Tracks = append(result.Tracks, track)
		}
		if len(result.Tracks) == total {
			return result, nil
		}
		if len(items) < playlistPageSize {
			return empty, errors.New("QQ playlist is incomplete: a page ended before the declared total")
		}
	}
}

// Search returns the requested song-offset window from direct QQ search.
// Upstream uses page numbers, so fixed-size pages are sliced locally.
func (c *Client) Search(ctx context.Context, keyword string, limit, offset int) ([]model.Track, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}
	keyword = strings.TrimSpace(keyword)
	if keyword == "" || len(keyword) > 1000 || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, errors.New("QQ search requires a keyword, limit 1-100 and offset 0-100000")
	}
	tracks := make([]model.Track, 0, limit)
	page, skip := offset/searchPageSize+1, offset%searchPageSize
	seen := make(map[string]bool)
	for requests := 0; len(tracks) < limit && requests < 3; requests++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		searchID, err := randomSearchID()
		if err != nil {
			return nil, err
		}
		payload := map[string]any{
			"comm": map[string]any{"_channelid": "0", "_os_version": "6.2.9200-2", "ct": "19", "cv": "2151", "guid": searchID[:32], "patch": "118", "psrf_access_token_expiresAt": 0, "psrf_qqaccess_token": "", "psrf_qqopenid": "", "psrf_qqunionid": "", "tmeAppID": "qqmusic", "tmeLoginType": 0, "uin": "0", "wid": "0"},
			"music.search.SearchCgiService": map[string]any{
				"module": "music.search.SearchCgiService", "method": "DoSearchForQQMusicDesktop",
				"param": map[string]any{"grp": 1, "num_per_page": searchPageSize, "page_num": page, "query": keyword, "remoteplace": "txt.newclient.top", "search_type": 0, "searchid": searchID},
			},
		}
		root, err := c.post(ctx, "/cgi-bin/musics.fcg", payload, "", true)
		if err != nil {
			return nil, fmt.Errorf("search QQ platform: %w", err)
		}
		data, err := responseData(root, "music.search.SearchCgiService")
		if err != nil {
			return nil, err
		}
		body := platform.Object(data["body"])
		song := platform.Object(body["song"])
		items, ok := song["list"].([]any)
		if !ok || len(items) > searchPageSize {
			return nil, errors.New("QQ search returned an invalid song list")
		}
		for i, item := range items {
			track, err := decodeTrack(item)
			if err != nil {
				return nil, err
			}
			if seen[track.ID] {
				return nil, errors.New("QQ search repeated a song page")
			}
			seen[track.ID] = true
			if i >= skip && len(tracks) < limit {
				tracks = append(tracks, track)
			}
		}
		if len(items) < searchPageSize {
			break
		}
		page++
		skip = 0
	}
	return tracks, nil
}

func (c *Client) post(ctx context.Context, path string, value any, playlistID string, signed bool) (map[string]any, error) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, errors.New("cannot encode QQ platform request")
	}
	payload := bytes.TrimSuffix(body.Bytes(), []byte{'\n'})
	endpoint := c.baseURL + path
	if signed {
		endpoint += "?sign=" + signRequest(payload)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Origin", "https://y.qq.com")
	if signed {
		headers.Set("User-Agent", "QQMusic 14090508(android 12)")
	}
	headers.Set("Referer", "https://y.qq.com/")
	if playlistID != "" {
		headers.Set("Referer", "https://y.qq.com/n/yqq/playsquare/"+playlistID+".html")
	}
	if c.cookie != "" {
		headers.Set("Cookie", c.cookie)
	}
	return c.http.JSON(ctx, http.MethodPost, endpoint, payload, headers)
}

func responseData(root map[string]any, requestKey string) (map[string]any, error) {
	code, ok := platform.Integer(root["code"])
	if !ok || code != 0 {
		return nil, errors.New("QQ platform rejected the metadata request")
	}
	response := platform.Object(root[requestKey])
	if response == nil && requestKey == "music.search.SearchCgiService" {
		response = platform.Object(root["req"])
	}
	code, ok = platform.Integer(response["code"])
	if !ok || code != 0 {
		return nil, errors.New("QQ platform rejected the metadata subrequest")
	}
	data := platform.Object(response["data"])
	if data == nil {
		return nil, errors.New("QQ platform returned no metadata object")
	}
	return data, nil
}

func decodeTrack(raw any) (model.Track, error) {
	var empty model.Track
	obj, ok := raw.(map[string]any)
	if !ok {
		return empty, errors.New("QQ platform returned a malformed song")
	}
	mid, ok := obj["mid"].(string)
	if !ok || !midPattern.MatchString(mid) {
		return empty, errors.New("QQ platform returned no valid string song MID")
	}
	track := model.Track{ID: mid, Name: platform.Text(obj, "title", "name"), Artists: platform.Artists(obj["singer"]), Album: platform.Text(platform.Object(obj["album"]), "name")}
	if rawDuration, found := obj["interval"]; found {
		seconds, ok := platform.Integer(rawDuration)
		if !ok || seconds < 0 || seconds > 10000000 {
			return empty, errors.New("QQ platform returned an invalid song duration")
		}
		track.Duration = int(seconds)
	}
	return track, nil
}

func randomSearchID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errors.New("cannot initialize QQ search request")
	}
	return strings.ToUpper(hex.EncodeToString(data[:])) + "00000", nil
}

// QQ's web/desktop metadata signature is derived from the exact JSON bytes.
// This is an independent Go implementation of the protocol observed in LX.
func signRequest(payload []byte) string {
	digest := sha1.Sum(payload)
	hash := hex.EncodeToString(digest[:])
	pick := func(indexes []int) string {
		var out strings.Builder
		for _, index := range indexes {
			if index < len(hash) {
				out.WriteByte(hash[index])
			}
		}
		return out.String()
	}
	mask := [20]byte{89, 39, 179, 150, 218, 82, 58, 252, 177, 52, 186, 123, 120, 64, 242, 133, 143, 161, 121, 179}
	for i := range mask {
		mask[i] ^= digest[i]
	}
	encoded := strings.NewReplacer("/", "", "+", "", "=", "").Replace(base64.StdEncoding.EncodeToString(mask[:]))
	return strings.ToLower("zzc" + pick([]int{23, 14, 6, 36, 16, 40, 7, 19}) + encoded + pick([]int{16, 1, 32, 12, 19, 27, 8, 5}))
}
