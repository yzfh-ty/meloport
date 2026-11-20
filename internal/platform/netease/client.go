// Package netease reads playlist and search metadata directly from NetEase.
// The fixed crypto constants implement its public client protocol; they are not
// personal API keys. Audio resolution is deliberately outside this package.
package netease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"meloport/internal/model"
	"meloport/internal/platform"
)

const (
	maxPlaylistTracks = 100000
	detailBatchSize   = 1000
	maxRankings       = 1000
)

type Options struct {
	HTTP    *platform.HTTP
	BaseURL string
	Cookie  string
}

type Client struct {
	http       *platform.HTTP
	base       string
	searchBase string
	cookie     string
}

func New(opts Options) *Client {
	if opts.HTTP == nil {
		opts.HTTP = platform.NewHTTP(nil, 30*time.Second, time.Second)
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	searchBase := base
	if base == "" {
		base, searchBase = "https://music.163.com", "https://interface.music.163.com"
	}
	return &Client{http: opts.HTTP, base: base, searchBase: searchBase, cookie: opts.Cookie}
}

func (c *Client) headers() http.Header {
	h := http.Header{
		"Content-Type": {"application/x-www-form-urlencoded"},
		"Accept":       {"application/json"},
		"User-Agent":   {"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36"},
		"Origin":       {"https://music.163.com"},
		"Referer":      {"https://music.163.com/"},
	}
	if c.cookie != "" {
		h.Set("Cookie", c.cookie)
	}
	return h
}

func (c *Client) request(ctx context.Context, endpoint string, payload []byte) (map[string]any, error) {
	root, err := c.http.JSON(ctx, http.MethodPost, c.base+endpoint, payload, c.headers())
	if err != nil {
		return nil, err
	}
	if err := responseOK(root); err != nil {
		return nil, err
	}
	return root, nil
}

func (c *Client) Playlist(ctx context.Context, id string) (model.Playlist, error) {
	if !validID(id) {
		return model.Playlist{}, errors.New("NetEase playlist ID must be a positive integer")
	}
	payload, err := linuxForm("/api/v3/playlist/detail", map[string]any{"id": id, "n": maxPlaylistTracks, "s": 8})
	if err != nil {
		return model.Playlist{}, err
	}
	root, err := c.request(ctx, "/api/linux/forward", payload)
	if err != nil {
		return model.Playlist{}, err
	}
	object, ok := root["playlist"].(map[string]any)
	if !ok {
		return model.Playlist{}, errors.New("NetEase response has no playlist object")
	}
	if returned := text(object, "id"); returned != "" && returned != id {
		return model.Playlist{}, errors.New("NetEase returned a different playlist ID")
	}
	rawIDs, ok := object["trackIds"].([]any)
	if !ok {
		return model.Playlist{}, errors.New("NetEase playlist has no authoritative track ID list")
	}
	if len(rawIDs) > maxPlaylistTracks {
		return model.Playlist{}, errors.New("NetEase playlist exceeds 100000-track safety limit")
	}
	if rawCount, exists := object["trackCount"]; exists {
		count, valid := integer(rawCount)
		if !valid || count != int64(len(rawIDs)) {
			return model.Playlist{}, errors.New("NetEase playlist track count does not match member IDs")
		}
	}
	ids := make([]string, 0, len(rawIDs))
	wanted := make(map[string]bool, len(rawIDs))
	for _, raw := range rawIDs {
		object, ok := raw.(map[string]any)
		if !ok {
			return model.Playlist{}, errors.New("NetEase playlist contains an invalid member")
		}
		member := text(object, "id")
		if !validID(member) {
			return model.Playlist{}, errors.New("NetEase playlist contains an invalid song ID")
		}
		ids = append(ids, member)
		wanted[member] = true
	}
	details := make(map[string]model.Track, len(ids))
	if raw, present := object["tracks"]; present && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return model.Playlist{}, errors.New("NetEase embedded tracks must be an array")
		}
		for _, item := range list {
			track, err := parseTrack(item)
			if err != nil {
				return model.Playlist{}, err
			}
			if !wanted[track.ID] {
				return model.Playlist{}, errors.New("NetEase embedded song is not a playlist member")
			}
			details[track.ID] = track
		}
	}
	missing := make([]string, 0)
	requested := make(map[string]bool)
	for _, id := range ids {
		if _, exists := details[id]; !exists && !requested[id] {
			missing = append(missing, id)
			requested[id] = true
		}
	}
	for start := 0; start < len(missing); start += detailBatchSize {
		end := min(start+detailBatchSize, len(missing))
		batch := missing[start:end]
		tracks, err := c.songDetails(ctx, batch)
		if err != nil {
			return model.Playlist{}, err
		}
		for _, track := range tracks {
			details[track.ID] = track
		}
	}
	result := model.Playlist{ID: id, Name: text(object, "name"), ReportedCount: len(ids), Tracks: make([]model.Track, 0, len(ids))}
	for _, id := range ids {
		track, exists := details[id]
		if !exists {
			return model.Playlist{}, errors.New("NetEase did not return all playlist member details")
		}
		result.Tracks = append(result.Tracks, track)
	}
	return result, nil
}

// Rankings reads the public chart directory exposed by the NetEase web client.
// NetEase chart IDs are playlist IDs, so Ranking can reuse the validated native
// playlist reader below without involving the ChKSz gateway.
func (c *Client) Rankings(ctx context.Context) ([]model.Ranking, error) {
	// NetEase currently emits updateFrequency twice in each chart object: an
	// initial null field followed by the populated value. The platform HTTP
	// client otherwise rejects duplicate keys, so permit this one known field;
	// all other duplicate fields remain invalid.
	root, err := c.http.JSONAllowDuplicateKeys(ctx, http.MethodGet, c.base+"/api/toplist", nil, c.headers(), "updateFrequency")
	if err != nil {
		return nil, err
	}
	if err := responseOK(root); err != nil {
		return nil, err
	}
	raw, ok := root["list"].([]any)
	if !ok || len(raw) > maxRankings {
		return nil, errors.New("NetEase ranking catalog is invalid")
	}
	items := make([]model.Ranking, 0, len(raw))
	for _, value := range raw {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("NetEase ranking catalog contains an invalid entry")
		}
		id := text(object, "id")
		if !validID(id) {
			return nil, errors.New("NetEase ranking catalog contains an invalid ID")
		}
		name := text(object, "name")
		if name == "" {
			return nil, errors.New("NetEase ranking catalog contains an unnamed entry")
		}
		items = append(items, model.Ranking{ID: id, Name: name, TrackCount: intValue(object["trackCount"]), UpdateFrequency: text(object, "updateFrequency")})
	}
	return items, nil
}

func (c *Client) Ranking(ctx context.Context, id string) (model.Playlist, error) {
	return c.Playlist(ctx, id)
}

func intValue(value any) int {
	n, ok := integer(value)
	if !ok || n < 0 || n > int64(maxPlaylistTracks) {
		return 0
	}
	return int(n)
}

func (c *Client) songDetails(ctx context.Context, ids []string) ([]model.Track, error) {
	entries := make([]map[string]json.Number, len(ids))
	wanted := make(map[string]bool, len(ids))
	for i, id := range ids {
		entries[i] = map[string]json.Number{"id": json.Number(id)}
		wanted[id] = true
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return nil, errors.New("cannot encode NetEase detail IDs")
	}
	payload, err := weapiForm(map[string]any{"c": string(encoded), "ids": "[" + strings.Join(ids, ",") + "]"})
	if err != nil {
		return nil, err
	}
	root, err := c.request(ctx, "/weapi/v3/song/detail", payload)
	if err != nil {
		return nil, err
	}
	items, ok := root["songs"].([]any)
	if !ok {
		return nil, errors.New("NetEase detail response has no songs array")
	}
	result := make([]model.Track, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		track, err := parseTrack(item)
		if err != nil {
			return nil, err
		}
		if !wanted[track.ID] {
			return nil, errors.New("NetEase returned an unrequested song detail")
		}
		if seen[track.ID] {
			continue
		}
		seen[track.ID] = true
		result = append(result, track)
	}
	if len(seen) != len(wanted) {
		return nil, errors.New("NetEase returned incomplete playlist song details")
	}
	return result, nil
}

func (c *Client) Search(ctx context.Context, keyword string, limit, offset int) ([]model.Track, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" || len([]rune(keyword)) > 500 || limit < 1 || limit > 100 || offset < 0 || offset > maxPlaylistTracks {
		return nil, errors.New("invalid NetEase search parameters")
	}
	payload, err := eapiForm("/api/search/song/list/page", map[string]any{"keyword": keyword, "needCorrect": "1", "channel": "typing", "offset": offset, "scene": "normal", "total": offset == 0, "limit": limit})
	if err != nil {
		return nil, err
	}
	root, err := c.http.JSON(ctx, http.MethodPost, c.searchBase+"/eapi/batch", payload, c.headers())
	if err != nil {
		return nil, err
	}
	if err := responseOK(root); err != nil {
		return nil, err
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return nil, errors.New("NetEase search response has no data object")
	}
	items, ok := data["resources"].([]any)
	if !ok {
		if count, valid := integer(data["totalCount"]); valid && count == 0 && data["resources"] == nil {
			return []model.Track{}, nil
		}
		return nil, errors.New("NetEase search response has no resource list")
	}
	if len(items) > 1000 {
		return nil, errors.New("NetEase search response exceeds resource safety limit")
	}
	if len(items) == 0 {
		if count, valid := integer(data["totalCount"]); valid && int64(offset) < count {
			return nil, errors.New("NetEase search returned an incomplete result page")
		}
	}
	result := make([]model.Track, 0, min(len(items), limit))
	seen := make(map[string]bool)
	for _, item := range items {
		resource, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("NetEase search contains an invalid resource")
		}
		base, ok := resource["baseInfo"].(map[string]any)
		if !ok {
			return nil, errors.New("NetEase search contains no song metadata")
		}
		track, err := parseTrack(base["simpleSongData"])
		if err != nil {
			return nil, err
		}
		if seen[track.ID] {
			continue
		}
		seen[track.ID] = true
		if len(result) < limit {
			result = append(result, track)
		}
	}
	return result, nil
}

func responseOK(root map[string]any) error {
	code, ok := integer(root["code"])
	if !ok {
		return errors.New("NetEase response has no valid business status")
	}
	if code != 200 {
		return fmt.Errorf("NetEase metadata request failed with code %d", code)
	}
	return nil
}

func parseTrack(raw any) (model.Track, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return model.Track{}, errors.New("NetEase song detail is not an object")
	}
	id := text(object, "id")
	if !validID(id) {
		return model.Track{}, errors.New("NetEase song detail has no valid ID")
	}
	track := model.Track{ID: id, Name: text(object, "name"), Artists: []string{}}
	if milliseconds, valid := integer(object["dt"]); valid && milliseconds >= 0 {
		track.Duration = int(milliseconds / 1000)
	}
	for _, key := range []string{"ar", "artists"} {
		if artists, ok := object[key].([]any); ok {
			for _, raw := range artists {
				if artist, ok := raw.(map[string]any); ok {
					if name := text(artist, "name"); name != "" {
						track.Artists = append(track.Artists, name)
					}
				}
			}
			break
		}
	}
	for _, key := range []string{"al", "album"} {
		if album, ok := object[key].(map[string]any); ok {
			track.Album = text(album, "name")
			break
		}
	}
	if cloud, ok := object["pc"].(map[string]any); ok {
		if title := text(cloud, "sn"); title != "" {
			track.Name = title
		}
		if artist := text(cloud, "ar"); artist != "" {
			track.Artists = []string{artist}
		}
		if album := text(cloud, "alb"); album != "" {
			track.Album = album
		}
	}
	return track, nil
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 20 || value[0] == '0' {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func text(object map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := object[key].(type) {
		case string:
			if value != "" {
				return value
			}
		case json.Number:
			return value.String()
		}
	}
	return ""
}

func integer(value any) (int64, bool) {
	var raw string
	switch v := value.(type) {
	case json.Number:
		raw = v.String()
	case string:
		raw = v
	default:
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil
}
