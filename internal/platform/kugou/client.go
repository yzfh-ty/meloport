// Package kugou reads playlist members and search metadata directly from Kugou.
// Audio resolution remains a separate concern; native hashes are not assumed to
// be interchangeable with the identifiers accepted by a third-party resolver.
package kugou

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"meloport/internal/model"
	"meloport/internal/platform"
)

const (
	maxTracks      = 10000
	maxPages       = 100
	chainPageSize  = 50
	globalPageSize = 100
	maxRankings    = 1000
	// Public platform protocol constants, not a user's ChKSz key or cookie.
	webSigningMaterial     = "NVPh5oo715z5DIWAeQlhMDsWXXQV4hwt"
	androidSigningMaterial = "OIlwieks28dk2k092lksi2UIkp"
)

var (
	digits                = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	token                 = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	hashPattern           = regexp.MustCompile(`^[A-Fa-f0-9]{32}$`)
	specialPath           = regexp.MustCompile(`/(?:yy/)?special/single/([1-9][0-9]{0,19})\.html$`)
	encodedPath           = regexp.MustCompile(`(?:^|/)(gcid_[A-Za-z0-9_-]+)(?:/|$)`)
	globalData            = regexp.MustCompile(`(?s)global\.data\s*=\s*(\[.*?\])\s*;`)
	dataAssignment        = regexp.MustCompile(`\bvar\s+data\s*=\s*`)
	specialInfoAssignment = regexp.MustCompile(`\bvar\s+specialInfo\s*=\s*`)
	globalName            = regexp.MustCompile(`(?s)global\s*=\s*\{.*?\bname\s*:\s*"((?:\\.|[^"\\])*)"`)
	globalCount           = regexp.MustCompile(`(?s)global\s*=\s*\{.*?\b(?:songcount|song_count|count|total)\s*:\s*([0-9]+)`)
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
}

func New(options Options) *Client {
	h := options.HTTP
	if h == nil {
		h = platform.NewHTTP(nil, 30*time.Second, time.Second)
	}
	return &Client{http: h, baseURL: strings.TrimRight(options.BaseURL, "/"), cookie: options.Cookie}
}

// NormalizePlaylist keeps the platform's different identifier families distinct.
// A bare numeric value is a share code. Use id_123 for a special playlist ID.
func NormalizePlaylist(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if digits.MatchString(raw) {
		return raw, nil
	}
	if strings.HasPrefix(raw, "id_") && digits.MatchString(strings.TrimPrefix(raw, "id_")) {
		return raw, nil
	}
	if strings.HasPrefix(raw, "gcid_") && token.MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" || len(raw) > 4096 {
		return "", playlistInputError()
	}
	host := strings.ToLower(u.Hostname())
	if host != "kugou.com" && !strings.HasSuffix(host, ".kugou.com") {
		return "", playlistInputError()
	}
	if match := specialPath.FindStringSubmatch(u.Path); len(match) > 0 {
		return "id_" + match[1], nil
	}
	query := u.Query()
	gcid, chain := query.Get("global_collection_id"), query.Get("chain")
	if gcid != "" || chain != "" {
		if (gcid != "" && !token.MatchString(gcid)) || (chain != "" && !token.MatchString(chain)) {
			return "", playlistInputError()
		}
		clean := url.Values{}
		if gcid != "" {
			clean.Set("global_collection_id", gcid)
		}
		if chain != "" {
			clean.Set("chain", chain)
		}
		return "https://m3ws.kugou.com/share/index.php?" + clean.Encode(), nil
	}
	if match := encodedPath.FindStringSubmatch(u.Path); len(match) > 0 && token.MatchString(match[1]) {
		return match[1], nil
	}
	return "", playlistInputError()
}

func playlistInputError() error {
	return errors.New("Kugou playlist must be a numeric share code, id_SPECIAL_ID, encoded gcid, or a full official URL containing a special ID, chain or global_collection_id; expand unsupported short links first")
}

func (c *Client) Playlist(ctx context.Context, raw string) (model.Playlist, error) {
	id, err := NormalizePlaylist(raw)
	if err != nil {
		return model.Playlist{}, err
	}
	if strings.HasPrefix(id, "id_") {
		return c.special(ctx, id, strings.TrimPrefix(id, "id_"))
	}
	if digits.MatchString(id) {
		return c.shareCode(ctx, id)
	}
	if strings.HasPrefix(id, "gcid_") {
		gcid, err := c.decodeGlobalID(ctx, id)
		if err != nil {
			return model.Playlist{}, err
		}
		return c.global(ctx, id, gcid)
	}
	u, _ := url.Parse(id)
	gcid, chain := u.Query().Get("global_collection_id"), u.Query().Get("chain")
	if gcid != "" && chain != "" {
		return c.chain(ctx, id, gcid, chain)
	}
	if gcid != "" {
		return c.global(ctx, id, gcid)
	}
	return c.resolveChain(ctx, id, chain)
}

// Rankings reads the public mobile chart directory. The returned rankid is a
// Kugou chart identity and is intentionally not treated as a playlist ID.
func (c *Client) Rankings(ctx context.Context) ([]model.Ranking, error) {
	uri := c.endpoint("https://m.kugou.com", "/rank/list?json=true")
	headers := http.Header{"Accept": {"application/json"}, "Referer": {"https://m.kugou.com/"}}
	root, err := c.http.JSON(ctx, http.MethodGet, uri, nil, headers)
	if err != nil {
		return nil, err
	}
	rank := platform.Object(root["rank"])
	raw := platform.Slice(rank["list"])
	if raw == nil || len(raw) > maxRankings {
		return nil, errors.New("Kugou ranking catalog is invalid")
	}
	if total, ok := platform.Integer(rank["total"]); !ok || total < 0 || int(total) != len(raw) {
		return nil, errors.New("Kugou ranking catalog is incomplete")
	}
	items := make([]model.Ranking, 0, len(raw))
	for _, value := range raw {
		object := platform.Object(value)
		if object == nil {
			return nil, errors.New("Kugou ranking catalog contains an invalid entry")
		}
		id := platform.Text(object, "rankid", "rank_id")
		if !digits.MatchString(id) {
			return nil, errors.New("Kugou ranking catalog contains an invalid ID")
		}
		if playable, ok := platform.Integer(object["isvol"]); !ok || playable != 1 {
			continue
		}
		name := platform.Text(object, "rankname", "name")
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("Kugou ranking catalog contains an unnamed entry")
		}
		extra := platform.Object(platform.Object(object["extra"])["resp"])
		items = append(items, model.Ranking{ID: id, Name: name, TrackCount: intField(extra, "all_total"), UpdateFrequency: platform.Text(object, "update_frequency", "updateFrequency")})
	}
	return items, nil
}

// Ranking reads native chart members and preserves the Kugou hash/audio_id
// identity needed by the resolver's later identity confirmation step.
func (c *Client) Ranking(ctx context.Context, id string) (model.Playlist, error) {
	if !digits.MatchString(id) {
		return model.Playlist{}, errors.New("Kugou ranking ID must be a positive integer")
	}
	tracks := make([]model.Track, 0, 100)
	seen := make(map[string]bool)
	total := -1
	name := ""
	for page := 1; page <= maxPages; page++ {
		query := url.Values{"version": {"9108"}, "ranktype": {"1"}, "plat": {"0"}, "pagesize": {"100"}, "area_code": {"1"}, "page": {strconv.Itoa(page)}, "rankid": {id}, "with_res_tag": {"0"}, "show_portrait_mv": {"1"}}
		value, err := c.request(ctx, http.MethodGet, "https://mobilecdnbj.kugou.com", "/api/v3/rank/song", query, nil, false, false)
		if err != nil {
			return model.Playlist{}, err
		}
		data := platform.Object(value)
		raw := platform.Slice(data["info"])
		if raw == nil {
			raw = platform.Slice(data["list"])
		}
		if raw == nil || len(raw) > 100 {
			return model.Playlist{}, errors.New("Kugou ranking song list is invalid")
		}
		if declared, ok := platform.Integer(data["total"]); ok {
			if declared < 0 || declared > maxTracks {
				return model.Playlist{}, errors.New("Kugou ranking total is invalid")
			}
			if total < 0 {
				total = int(declared)
			} else if total != int(declared) {
				return model.Playlist{}, errors.New("Kugou ranking changed during pagination")
			}
		}
		if name == "" {
			name = platform.Text(data, "rankname", "name")
		}
		for _, item := range raw {
			track, err := rankingTrack(item)
			if err != nil {
				return model.Playlist{}, err
			}
			if seen[track.ID] {
				return model.Playlist{}, errors.New("Kugou ranking contains duplicate song identities")
			}
			seen[track.ID] = true
			tracks = append(tracks, track)
		}
		if total >= 0 {
			if len(tracks) == total {
				break
			}
			if len(tracks) > total || len(raw) == 0 {
				return model.Playlist{}, errors.New("Kugou ranking pagination is incomplete")
			}
		} else if len(raw) < 100 {
			break
		}
		if page == maxPages {
			return model.Playlist{}, errors.New("Kugou ranking exceeds the bounded pagination budget")
		}
	}
	if total < 0 {
		total = len(tracks)
	}
	return model.Playlist{ID: id, Name: name, Tracks: tracks, ReportedCount: total}, nil
}

func intField(object map[string]any, fields ...string) int {
	for _, field := range fields {
		if n, ok := platform.Integer(object[field]); ok && n >= 0 && n <= maxTracks {
			return int(n)
		}
	}
	return 0
}

func rankingTrack(raw any) (model.Track, error) {
	object := platform.Object(raw)
	if object == nil {
		return model.Track{}, errors.New("Kugou ranking returned a malformed song")
	}
	hash := strings.ToLower(platform.Text(object, "hash", "FileHash"))
	audioID := platform.Text(object, "audio_id", "Audioid", "audioid")
	if !hashPattern.MatchString(hash) && !digits.MatchString(audioID) {
		return model.Track{}, errors.New("Kugou ranking song has no valid hash or audio ID")
	}
	id := hash
	if id == "" {
		id = audioID
	}
	name := html.UnescapeString(platform.Text(object, "songname", "SongName", "name"))
	artists := platform.Artists(object["authors"])
	if len(artists) == 0 {
		artists = splitArtists(platform.Text(object, "author_name", "SingerName", "singername", "author"))
	}
	for i := range artists {
		artists[i] = html.UnescapeString(artists[i])
	}
	if strings.TrimSpace(name) == "" || len(artists) == 0 {
		return model.Track{}, errors.New("Kugou ranking song has incomplete metadata")
	}
	album := html.UnescapeString(platform.Text(object, "album_name", "AlbumName", "remark"))
	duration := 0
	if value, ok := platform.Integer(object["duration"]); ok && value >= 0 && value <= 24*60*60 {
		duration = int(value)
	} else if value, ok := platform.Integer(object["timelength"]); ok && value >= 0 && value <= 24*60*60*1000 {
		duration = int(value / 1000)
	}
	track := model.Track{ID: id, Hash: hash, Name: name, Artists: artists, Album: album, Duration: duration}
	addAlbumTags(&track, object)
	return track, nil
}

func (c *Client) endpoint(origin, path string) string {
	if c.baseURL != "" {
		return c.baseURL + path
	}
	return origin + path
}

func (c *Client) headers() http.Header {
	h := http.Header{"User-Agent": {"Mozilla/5.0 (iPhone; CPU iPhone OS 11_0 like Mac OS X) AppleWebKit/604.1.38 Mobile/15A372 Safari/604.1"}, "Referer": {"https://m3ws.kugou.com/share/index.php"}, "Accept": {"application/json"}}
	if c.cookie != "" {
		h.Set("Cookie", c.cookie)
	}
	return h
}

func (c *Client) request(ctx context.Context, method, origin, path string, query url.Values, payload []byte, signed bool, android bool) (any, error) {
	if query == nil {
		query = url.Values{}
	}
	if signed {
		key := webSigningMaterial
		if android {
			key = androidSigningMaterial
		}
		query.Set("signature", signature(query.Encode(), key, string(payload)))
	}
	uri := c.endpoint(origin, path)
	if len(query) > 0 {
		uri += "?" + query.Encode()
	}
	h := c.headers()
	// Never send an optional platform cookie over a clear-text endpoint. This
	// matters for the legacy mobile rank API, which is still HTTP in some regions.
	if parsed, parseErr := url.Parse(uri); parseErr == nil && parsed.Scheme != "https" {
		h.Del("Cookie")
	}
	if payload != nil {
		h.Set("Content-Type", "application/json")
	}
	if path == "/v3/album_audio/audio" {
		h.Set("KG-THash", "13a3164")
		h.Set("KG-RC", "1")
		h.Set("KG-Fake", "0")
		h.Set("KG-RF", "00869891")
		h.Set("User-Agent", "Android712-AndroidPhone-11451-376-0-FeeCacheUpdate-wifi")
		h.Set("x-router", "kmr.service.kugou.com")
	}
	root, err := c.http.JSON(ctx, method, uri, payload, h)
	if err != nil {
		return nil, fmt.Errorf("Kugou metadata: %w", err)
	}
	return unwrap(root)
}

func unwrap(root map[string]any) (any, error) {
	found := false
	for _, field := range []string{"error_code", "errcode", "err_code"} {
		if raw, exists := root[field]; exists {
			code, ok := platform.Integer(raw)
			if !ok || code != 0 {
				return nil, errors.New("Kugou metadata business request failed")
			}
			found = true
		}
	}
	if status, exists := root["status"]; exists {
		n, ok := platform.Integer(status)
		if !ok || n != 1 {
			return nil, errors.New("Kugou metadata business request failed")
		}
		found = true
	}
	if !found {
		return nil, errors.New("Kugou metadata has no recognized success status")
	}
	if data, exists := root["data"]; exists && data != nil {
		return data, nil
	}
	if _, ok := root["info"].([]any); ok {
		return root, nil
	}
	if info := platform.Object(root["info"]); info != nil {
		return info, nil
	}
	return nil, errors.New("Kugou metadata payload is missing")
}

func signature(query, key, body string) string {
	parts := strings.Split(query, "&")
	sort.Strings(parts)
	sum := md5.Sum([]byte(key + strings.Join(parts, "") + body + key))
	return hex.EncodeToString(sum[:])
}

func baseParameters() url.Values {
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	return url.Values{"srcappid": {"2919"}, "clientver": {"20000"}, "clienttime": {now}, "mid": {now}, "uuid": {now}, "dfid": {"-"}, "appid": {"1058"}}
}

func (c *Client) chain(ctx context.Context, identity, gcid, chain string) (model.Playlist, error) {
	var raw []any
	name, total := "", -1
	seen := map[string]bool{}
	for page := 1; page <= maxPages; page++ {
		q := baseParameters()
		q.Set("listid", "15")
		q.Set("type", "0")
		q.Set("pagesize", strconv.Itoa(chainPageSize))
		q.Set("global_collection_id", gcid)
		q.Set("page", strconv.Itoa(page))
		q.Set("share_type", "collect")
		q.Set("chain", chain)
		value, err := c.request(ctx, http.MethodGet, "https://m3ws.kugou.com", "/v2/zlist/list", q, nil, true, false)
		if err != nil {
			return model.Playlist{}, err
		}
		data := platform.Object(value)
		infos, ok := data["info"].([]any)
		if !ok || len(infos) != 1 {
			return model.Playlist{}, errors.New("Kugou playlist information is missing or ambiguous")
		}
		info := platform.Object(infos[0])
		count, err := countField(info, "count")
		if err != nil {
			return model.Playlist{}, err
		}
		if page == 1 {
			total = count
			name = platform.Text(info, "name")
			if total > chainPageSize*maxPages {
				return model.Playlist{}, errors.New("Kugou playlist exceeds the bounded pagination budget")
			}
		} else if count != total {
			return model.Playlist{}, errors.New("Kugou playlist changed during pagination")
		}
		list, ok := platform.Object(data["list"])["info"].([]any)
		if !ok {
			return model.Playlist{}, errors.New("Kugou playlist member list is missing")
		}
		if err := appendPage(&raw, list, total, chainPageSize, seen); err != nil {
			return model.Playlist{}, err
		}
		if len(raw) == total {
			return c.buildPlaylist(ctx, identity, name, raw, total)
		}
	}
	return model.Playlist{}, errors.New("Kugou playlist exceeds the bounded pagination budget")
}

func (c *Client) global(ctx context.Context, identity, gcid string) (model.Playlist, error) {
	q := baseParameters()
	q.Set("specialid", "0")
	q.Set("global_specialid", gcid)
	q.Set("format", "json")
	value, err := c.request(ctx, http.MethodGet, "https://mobiles.kugou.com", "/api/v5/special/info_v2", q, nil, true, false)
	if err != nil {
		return model.Playlist{}, err
	}
	info := platform.Object(value)
	total, err := countField(info, "songcount")
	if err != nil {
		return model.Playlist{}, err
	}
	name := platform.Text(info, "specialname", "name")
	if total == 0 {
		return model.Playlist{ID: identity, Name: name, Tracks: []model.Track{}, ReportedCount: 0}, nil
	}
	var raw []any
	seen := map[string]bool{}
	for page := 1; page <= maxPages; page++ {
		q := baseParameters()
		q.Set("global_specialid", gcid)
		q.Set("specialid", "0")
		q.Set("plat", "0")
		q.Set("version", "8000")
		q.Set("page", strconv.Itoa(page))
		q.Set("pagesize", strconv.Itoa(globalPageSize))
		value, err := c.request(ctx, http.MethodGet, "https://mobiles.kugou.com", "/api/v5/special/song_v2", q, nil, true, false)
		if err != nil {
			return model.Playlist{}, err
		}
		data := platform.Object(value)
		list, ok := data["info"].([]any)
		if !ok {
			return model.Playlist{}, errors.New("Kugou playlist member list is missing")
		}
		if err := appendPage(&raw, list, total, globalPageSize, seen); err != nil {
			return model.Playlist{}, err
		}
		if len(raw) == total {
			return c.buildPlaylist(ctx, identity, name, raw, total)
		}
	}
	return model.Playlist{}, errors.New("Kugou playlist exceeds the bounded pagination budget")
}

func countField(object map[string]any, field string) (int, error) {
	n, ok := platform.Integer(object[field])
	if !ok || n < 0 || n > maxTracks {
		return 0, errors.New("Kugou playlist count is invalid or exceeds 10000 tracks")
	}
	return int(n), nil
}

func appendPage(all *[]any, list []any, total, pageSize int, seen map[string]bool) error {
	expected := min(pageSize, total-len(*all))
	if expected < 0 || len(list) != expected {
		return errors.New("Kugou playlist page is incomplete or inconsistent with its count")
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return errors.New("Kugou playlist contains an invalid page")
	}
	sum := md5.Sum(encoded)
	key := hex.EncodeToString(sum[:])
	if seen[key] && len(list) > 0 {
		return errors.New("Kugou playlist returned a repeated page")
	}
	seen[key] = true
	*all = append(*all, list...)
	return nil
}

func (c *Client) decodeGlobalID(ctx context.Context, encoded string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"ret_info": 1, "data": []any{map[string]any{"id": encoded, "id_type": 2}}})
	q := url.Values{"dfid": {"-"}, "appid": {"1005"}, "mid": {"0"}, "clientver": {"20109"}, "clienttime": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "uuid": {"-"}}
	value, err := c.request(ctx, http.MethodPost, "https://t.kugou.com", "/v1/songlist/batch_decode", q, payload, true, true)
	if err != nil {
		return "", err
	}
	list := platform.Slice(platform.Object(value)["list"])
	if len(list) != 1 {
		return "", errors.New("Kugou encoded playlist ID could not be resolved")
	}
	id := platform.Text(platform.Object(list[0]), "global_collection_id")
	if !token.MatchString(id) {
		return "", errors.New("Kugou encoded playlist ID could not be resolved")
	}
	return id, nil
}

func (c *Client) resolveChain(ctx context.Context, identity, chain string) (model.Playlist, error) {
	var raw []any
	name, total := "", -1
	seen := map[string]bool{}
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"pagesize": {strconv.Itoa(chainPageSize)}, "chain": {chain}, "su": {"1"}, "page": {strconv.Itoa(page)}, "n": {"0.7928855356604456"}}
		value, err := c.request(ctx, http.MethodGet, "https://m.kugou.com", "/schain/transfer", q, nil, false, false)
		if err != nil {
			return model.Playlist{}, err
		}
		data := platform.Object(value)
		if gcid := platform.Text(data, "global_collection_id"); token.MatchString(gcid) {
			return c.chain(ctx, identity, gcid, chain)
		}
		info := platform.Object(data["info"])
		count, err := countField(info, "count")
		if err != nil {
			return model.Playlist{}, err
		}
		if page == 1 {
			total = count
			name = platform.Text(info, "name")
			if total > chainPageSize*maxPages {
				return model.Playlist{}, errors.New("Kugou chain exceeds the bounded pagination budget")
			}
		} else if count != total {
			return model.Playlist{}, errors.New("Kugou chain changed during pagination")
		}
		list, ok := data["list"].([]any)
		if !ok {
			return model.Playlist{}, errors.New("Kugou chain has no recognizable member list")
		}
		if err := appendPage(&raw, list, total, chainPageSize, seen); err != nil {
			return model.Playlist{}, err
		}
		if len(raw) == total {
			return c.buildPlaylist(ctx, identity, name, raw, total)
		}
	}
	return model.Playlist{}, errors.New("Kugou chain exceeds the bounded pagination budget")
}

func (c *Client) shareCode(ctx context.Context, code string) (model.Playlist, error) {
	payload, _ := json.Marshal(map[string]any{"appid": 1001, "clientver": 9020, "mid": "21511157a05844bd085308bc76ef3343", "clienttime": 640612895, "key": "36164c4015e704673c588ee202b9ecb8", "data": code})
	value, err := c.request(ctx, http.MethodPost, "https://t.kugou.com", "/command/", nil, payload, false, false)
	if err != nil {
		return model.Playlist{}, err
	}
	data := platform.Object(value)
	info := platform.Object(data["info"])
	if info == nil {
		return model.Playlist{}, errors.New("Kugou share code response has no playlist information")
	}
	kind, kindOK := platform.Integer(info["type"])
	if !kindOK || kind != 2 {
		return model.Playlist{}, errors.New("Kugou share code does not identify a playlist")
	}
	if gcid := platform.Text(info, "global_collection_id"); token.MatchString(gcid) {
		return c.global(ctx, code, gcid)
	}
	if kind == 2 {
		if id := platform.Text(info, "id"); digits.MatchString(id) {
			return c.special(ctx, code, id)
		}
	}
	total, err := countField(info, "count")
	if err != nil {
		return model.Playlist{}, err
	}
	list, ok := data["list"].([]any)
	if !ok || len(list) != total {
		return model.Playlist{}, errors.New("Kugou share code does not expose a complete supported playlist; use its full playlist link")
	}
	return c.buildPlaylist(ctx, code, platform.Text(info, "name"), list, total)
}

func (c *Client) special(ctx context.Context, identity, id string) (model.Playlist, error) {
	// The former www2.kugou.kugou.com route no longer resolves reliably. The
	// official page now uses var data=[...], followed by another declaration in
	// the same statement, so a regexp ending at ]; would consume extra script.
	routes := []string{c.endpoint("https://www.kugou.com", "/yy/special/single/"+id+".html"), c.endpoint("https://www2.kugou.kugou.com", "/yueku/v9/special/single/"+id+"-5-9999.html")}
	var issues []error
	for _, route := range routes {
		body, err := c.http.Bytes(ctx, http.MethodGet, route, nil, c.headers())
		if err != nil {
			if ctx.Err() != nil {
				return model.Playlist{}, ctx.Err()
			}
			issues = append(issues, fmt.Errorf("Kugou special playlist: %w", err))
			continue
		}
		members, name, gcid, countVerified, err := parseSpecial(body, id)
		if err != nil {
			issues = append(issues, err)
			continue
		}
		// A declared count plus complete embedded details is sufficient without
		// another platform request. Otherwise prefer the paginated global route
		// when available, because an HTML array alone does not prove its total.
		if countVerified && completeEmbeddedMembers(members) {
			return c.buildPlaylist(ctx, identity, name, members, len(members))
		}
		if gcid != "" {
			playlist, err := c.global(ctx, identity, gcid)
			if err == nil {
				return playlist, nil
			}
			if ctx.Err() != nil {
				return model.Playlist{}, ctx.Err()
			}
			issues = append(issues, err)
		}
		playlist, err := c.buildPlaylist(ctx, identity, html.UnescapeString(name), members, len(members))
		if err == nil {
			return playlist, nil
		}
		if ctx.Err() != nil {
			return model.Playlist{}, ctx.Err()
		}
		issues = append(issues, err)
	}
	return model.Playlist{}, errors.Join(issues...)
}

func parseSpecial(body []byte, id string) ([]any, string, string, bool, error) {
	var members []any
	if starts := dataAssignment.FindAllIndex(body, -1); len(starts) == 1 {
		decoder := json.NewDecoder(bytes.NewReader(body[starts[0][1]:]))
		decoder.UseNumber()
		if err := decoder.Decode(&members); err != nil || members == nil {
			return nil, "", "", false, errors.New("Kugou embedded member list is invalid")
		}
	} else if len(starts) > 1 {
		return nil, "", "", false, errors.New("Kugou special playlist contains ambiguous data assignments")
	} else {
		match := globalData.FindSubmatch(body)
		if len(match) != 2 {
			return nil, "", "", false, errors.New("Kugou special playlist has no recognizable embedded member list")
		}
		decoder := json.NewDecoder(bytes.NewReader(match[1]))
		decoder.UseNumber()
		if err := decoder.Decode(&members); err != nil || members == nil {
			return nil, "", "", false, errors.New("Kugou embedded member list is invalid")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, "", "", false, errors.New("Kugou embedded member list is invalid")
		}
	}
	if len(members) >= 9999 {
		return nil, "", "", false, errors.New("Kugou special playlist may exceed the embedded-list limit")
	}
	countVerified := false
	if countMatch := globalCount.FindSubmatch(body); len(countMatch) == 2 {
		count, err := strconv.Atoi(string(countMatch[1]))
		if err != nil || count != len(members) {
			return nil, "", "", false, errors.New("Kugou special playlist embedded list is incomplete")
		}
		countVerified = true
	}
	name, gcid := "", ""
	if starts := specialInfoAssignment.FindAllIndex(body, -1); len(starts) == 1 {
		decoder := json.NewDecoder(bytes.NewReader(body[starts[0][1]:]))
		decoder.UseNumber()
		var info map[string]any
		if err := decoder.Decode(&info); err != nil || info == nil {
			return nil, "", "", false, errors.New("Kugou special playlist information is invalid")
		}
		if remote := platform.Text(info, "id"); remote != "" && remote != id {
			return nil, "", "", false, errors.New("Kugou special page returned a different playlist")
		}
		name = platform.Text(info, "name", "specialname")
		if value := platform.Text(info, "global_collection_id"); token.MatchString(value) {
			gcid = value
		}
		for _, field := range []string{"songcount", "song_count", "count", "total"} {
			if raw, present := info[field]; present {
				count, ok := platform.Integer(raw)
				if !ok || count != int64(len(members)) {
					return nil, "", "", false, errors.New("Kugou special playlist embedded list is incomplete")
				}
				countVerified = true
			}
		}
	} else if len(starts) > 1 {
		return nil, "", "", false, errors.New("Kugou special playlist contains ambiguous information assignments")
	}
	if name == "" {
		if match := globalName.FindSubmatch(body); len(match) == 2 {
			_ = json.Unmarshal(append(append([]byte{'"'}, match[1]...), '"'), &name)
		}
	}
	return members, name, gcid, countVerified, nil
}

func completeEmbeddedMembers(members []any) bool {
	for _, value := range members {
		item := platform.Object(value)
		hash := strings.ToLower(platform.Text(item, "hash", "FileHash"))
		if !hashPattern.MatchString(hash) {
			return false
		}
		if _, complete := embeddedTrack(item, hash); !complete {
			return false
		}
	}
	return true
}

func (c *Client) buildPlaylist(ctx context.Context, identity, name string, raw []any, total int) (model.Playlist, error) {
	if total < 0 || total > maxTracks || len(raw) != total {
		return model.Playlist{}, errors.New("Kugou playlist member count is incomplete")
	}
	hashes := make([]string, 0, len(raw))
	seen := map[string]bool{}
	details := map[string]model.Track{}
	for _, value := range raw {
		item := platform.Object(value)
		hash := strings.ToLower(platform.Text(item, "hash", "FileHash"))
		if !hashPattern.MatchString(hash) {
			return model.Playlist{}, errors.New("Kugou playlist member has no valid audio hash")
		}
		if track, complete := embeddedTrack(item, hash); complete {
			details[hash] = track
		}
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
	}
	// Current official pages already include complete title/artist/album/duration
	// metadata. Reuse it instead of depending on a second gateway lookup.
	missing := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		if _, present := details[hash]; !present {
			missing = append(missing, hash)
		}
	}
	hashes = missing
	for begin := 0; begin < len(hashes); begin += 100 {
		batch := hashes[begin:min(begin+100, len(hashes))]
		items := make([]any, 0, len(batch))
		for _, hash := range batch {
			items = append(items, map[string]string{"hash": hash})
		}
		payload, _ := json.Marshal(map[string]any{"area_code": "1", "show_privilege": 1, "show_album_info": "1", "is_publish": "", "appid": 1005, "clientver": 11451, "mid": "1", "dfid": "-", "clienttime": time.Now().UnixMilli(), "key": androidSigningMaterial, "fields": "album_info,author_name,audio_info,ori_audio_name,base,songname,classification,img,album_img", "data": items})
		value, err := c.request(ctx, http.MethodPost, "https://gateway.kugou.com", "/v3/album_audio/audio", nil, payload, false, false)
		if err != nil {
			return model.Playlist{}, err
		}
		rows, ok := value.([]any)
		if !ok {
			return model.Playlist{}, errors.New("Kugou song detail response is missing")
		}
		index := map[string]model.Track{}
		for _, row := range rows {
			if tuple, ok := row.([]any); ok {
				if len(tuple) == 0 {
					continue
				}
				row = tuple[0]
			}
			item := platform.Object(row)
			track, err := detailTrack(item)
			if err != nil {
				return model.Playlist{}, err
			}
			audio := platform.Object(item["audio_info"])
			for _, key := range []string{"hash", "hash_320", "hash_flac", "hash_high"} {
				hash := strings.ToLower(platform.Text(audio, key))
				if hashPattern.MatchString(hash) {
					index[hash] = track
				}
			}
		}
		for _, hash := range batch {
			track, ok := index[hash]
			if !ok {
				return model.Playlist{}, errors.New("Kugou song details do not cover every playlist member")
			}
			track.ID = hash
			track.Hash = hash
			details[hash] = track
		}
	}
	tracks := make([]model.Track, 0, len(raw))
	for _, value := range raw {
		hash := strings.ToLower(platform.Text(platform.Object(value), "hash", "FileHash"))
		tracks = append(tracks, details[hash])
	}
	return model.Playlist{ID: identity, Name: html.UnescapeString(name), Tracks: tracks, ReportedCount: total}, nil
}

func embeddedTrack(item map[string]any, hash string) (model.Track, bool) {
	name := html.UnescapeString(platform.Text(item, "songname"))
	artists := platform.Artists(item["authors"])
	if len(artists) == 0 {
		artists = splitArtists(platform.Text(item, "author_name", "singername"))
	}
	for i := range artists {
		artists[i] = html.UnescapeString(artists[i])
	}
	album := html.UnescapeString(platform.Text(item, "album_name"))
	ms, ok := platform.Integer(item["timelength"])
	if !ok {
		ms, ok = platform.Integer(item["duration"])
	}
	if strings.TrimSpace(name) == "" || len(artists) == 0 || strings.TrimSpace(album) == "" || !ok || ms <= 0 || ms > 24*60*60*1000 {
		return model.Track{}, false
	}
	track := model.Track{ID: hash, Hash: hash, Name: name, Artists: artists, Album: album, Duration: int(ms / 1000)}
	addAlbumTags(&track, item)
	return track, true
}

func detailTrack(item map[string]any) (model.Track, error) {
	audio := platform.Object(item["audio_info"])
	hash := strings.ToLower(platform.Text(audio, "hash"))
	if !hashPattern.MatchString(hash) {
		return model.Track{}, errors.New("Kugou song details have no valid hash")
	}
	name := html.UnescapeString(platform.Text(item, "songname", "ori_audio_name"))
	if strings.TrimSpace(name) == "" {
		return model.Track{}, errors.New("Kugou song details have no title")
	}
	track := model.Track{ID: hash, Hash: hash, Name: name, Artists: splitArtists(platform.Text(item, "author_name")), Album: html.UnescapeString(platform.Text(platform.Object(item["album_info"]), "album_name", "name"))}
	addAlbumTags(&track, item)
	if ms, ok := platform.Integer(audio["timelength"]); ok && ms >= 0 && ms <= 24*60*60*1000 {
		track.Duration = int(ms / 1000)
	}
	return track, nil
}

func addAlbumTags(track *model.Track, item map[string]any) {
	track.CoverURL = songCover(item)
	album := platform.Object(item["album_info"])
	track.AlbumArtists = splitArtists(platform.Text(album, "author_name"))
	for _, value := range []string{platform.Text(album, "publish_date"), platform.Text(item, "publish_date")} {
		if date, err := time.Parse("2006-01-02", value); err == nil && date.Year() >= 1900 && date.Year() <= 2100 {
			track.Year = date.Format("2006")
			break
		}
	}
}

// Covers from native Kugou responses use a literal {size} template. Only
// album/song cover fields are considered; singer avatars are not album art.
func songCover(item map[string]any) string {
	album := platform.Object(item["album_info"])
	audio := platform.Object(item["audio_info"])
	for _, value := range []string{
		platform.Text(item, "album_sizable_cover"),
		platform.Text(item, "album_img"),
		platform.Text(album, "sizable_cover"),
		platform.Text(album, "pic"),
		platform.Text(album, "img"),
		platform.Text(album, "s_img"),
		platform.Text(platform.Object(item["trans_param"]), "union_cover"),
		platform.Text(platform.Object(audio["trans_param"]), "union_cover"),
		platform.Text(item, "img"),
		platform.Text(item, "imgurl"),
	} {
		if cover := model.CanonicalCoverURL(strings.ReplaceAll(html.UnescapeString(value), "{size}", "800")); cover != "" {
			return cover
		}
	}
	return ""
}

func splitArtists(value string) []string {
	parts := strings.FieldsFunc(html.UnescapeString(value), func(r rune) bool { return r == '、' || r == '&' || r == '/' })
	var out []string
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (c *Client) Search(ctx context.Context, keyword string, limit, offset int) ([]model.Track, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" || len([]rune(keyword)) > 200 || limit < 1 || limit > 100 || offset < 0 || offset > 10000 {
		return nil, errors.New("invalid Kugou search keyword, limit or offset")
	}
	// Keep a fixed upstream page size even when the caller's offset is not aligned.
	const pageSize = 30
	first, last := offset/pageSize+1, (offset+limit-1)/pageSize+1
	var raw []any
	for page := first; page <= last; page++ {
		q := url.Values{"platform": {"AndroidFilter"}, "iscorrection": {"1"}, "keyword": {keyword}, "hifiquality": {"0"}, "pagesize": {strconv.Itoa(pageSize)}, "PrivilegeFilter": {"0"}, "page": {strconv.Itoa(page)}}
		value, err := c.request(ctx, http.MethodGet, "https://songsearch.kugou.com", "/song_search_v2", q, nil, false, false)
		if err != nil {
			return nil, err
		}
		data := platform.Object(value)
		list, ok := data["lists"].([]any)
		if !ok || len(list) > pageSize {
			return nil, errors.New("Kugou search returned an invalid song list")
		}
		raw = append(raw, list...)
		if len(list) < pageSize {
			break
		}
	}
	start := offset % pageSize
	if start >= len(raw) {
		return []model.Track{}, nil
	}
	raw = raw[start:min(start+limit, len(raw))]
	tracks := make([]model.Track, 0, len(raw))
	seen := map[string]bool{}
	for _, value := range raw {
		item := platform.Object(value)
		hash := strings.ToLower(platform.Text(item, "FileHash", "hash"))
		id := hash
		if !hashPattern.MatchString(hash) {
			hash = ""
			id = platform.Text(item, "Audioid", "audio_id")
		}
		if hash == "" && !digits.MatchString(id) {
			return nil, errors.New("Kugou search song has no valid identity")
		}
		name := platform.Text(item, "OriSongName", "SongName")
		if name == "" {
			return nil, errors.New("Kugou search song has no title")
		}
		if suffix := platform.Text(item, "Suffix"); suffix != "" && platform.Text(item, "OriSongName") != "" {
			name += " " + suffix
		}
		artists := platform.Artists(item["Singers"])
		if len(artists) == 0 {
			artists = splitArtists(platform.Text(item, "SingerName", "singername"))
		}
		for i := range artists {
			artists[i] = html.UnescapeString(artists[i])
		}
		track := model.Track{ID: id, Hash: hash, Name: html.UnescapeString(name), Artists: artists, Album: html.UnescapeString(platform.Text(item, "AlbumName"))}
		addAlbumTags(&track, item)
		if seconds, ok := platform.Integer(item["Duration"]); ok && seconds >= 0 && seconds <= 24*60*60 {
			track.Duration = int(seconds)
		}
		if !seen[id] {
			seen[id] = true
			tracks = append(tracks, track)
		}
	}
	return tracks, nil
}
