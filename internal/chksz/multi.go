package chksz

import (
	"context"
	"errors"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"meloport/internal/model"
)

var remoteIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// ResolvePlatform performs only audio resolution, using native quality names.
func (c *Client) ResolvePlatform(ctx context.Context, source, id, quality string) (model.ResolvedTrack, error) {
	if source == "netease" {
		return c.Resolve(ctx, id, quality)
	}
	if source != "qq" && source != "kugou" {
		return model.ResolvedTrack{}, errors.New("unsupported audio platform")
	}
	if !remoteIDPattern.MatchString(id) {
		return model.ResolvedTrack{}, errors.New("invalid audio source ID")
	}
	if quality == "" {
		quality = "flac"
	}
	switch quality {
	case "128k", "320k", "flac", "hires", "master":
	default:
		return model.ResolvedTrack{}, errors.New("unsupported platform audio quality")
	}
	endpoint, idKey := "/api/qq_music", "mid"
	if source == "kugou" {
		endpoint, idKey = "/api/kugou_music", "id"
	}
	root, err := c.get(ctx, endpoint, url.Values{idKey: {id}, "size": {quality}, "type": {"json"}})
	if err != nil {
		return model.ResolvedTrack{}, err
	}
	obj, err := c.unwrap(root, "song")
	if err != nil {
		return model.ResolvedTrack{}, err
	}
	return c.decodeAudio(obj)
}

func (c *Client) decodeAudio(obj map[string]any) (model.ResolvedTrack, error) {
	raw := textField(obj, "url", "musicUrl", "music_url", "playUrl", "play_url")
	u, err := url.Parse(raw)
	if err != nil || !validHTTPURL(u) || u.Fragment != "" {
		return model.ResolvedTrack{}, errors.New("ChKSz returned no valid HTTP(S) audio URL")
	}
	if c.containsKey(raw) || c.containsKey(u.Path) || strings.Contains(strings.ToLower(raw), "apikey=") {
		return model.ResolvedTrack{}, errors.New("ChKSz audio URL contains an API credential")
	}
	for name, values := range u.Query() {
		if strings.EqualFold(name, "apikey") {
			return model.ResolvedTrack{}, errors.New("ChKSz audio URL contains an API credential")
		}
		for _, value := range values {
			if c.containsKey(value) {
				return model.ResolvedTrack{}, errors.New("ChKSz audio URL contains an API credential")
			}
		}
	}
	format := strings.ToLower(strings.TrimPrefix(textField(obj, "format", "type", "ext"), "."))
	if !validFormat(format) {
		format = strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), ".")
		if !validFormat(format) {
			format = ""
		}
	}
	result := model.ResolvedTrack{URL: u.String(), Format: format}
	if rawSize, ok := first(obj, "size", "filesize", "fileSize"); ok {
		size, valid := integer(rawSize)
		if !valid || size < 0 {
			return model.ResolvedTrack{}, errors.New("ChKSz audio response contains an invalid file size")
		}
		result.Size = size
	}
	return result, nil
}

// Search is a metadata fallback. Native sources should be attempted first.
func (c *Client) Search(ctx context.Context, source, keyword string, limit, offset int) ([]model.Track, error) {
	if strings.TrimSpace(keyword) == "" || len([]rune(keyword)) > 300 || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, errors.New("invalid search keyword or pagination")
	}
	params := url.Values{}
	var endpoint string
	switch source {
	case "netease":
		endpoint = "/api/163_search"
		params.Set("keyword", keyword)
		params.Set("limit", strconv.Itoa(limit))
		params.Set("offset", strconv.Itoa(offset))
	case "qq", "kugou":
		if offset+limit > 50 {
			return nil, errors.New("ChKSz QQ/Kugou search fallback supports only the first 50 results")
		}
		endpoint = "/api/qq_music"
		if source == "kugou" {
			endpoint = "/api/kugou_music"
		}
		params.Set("msg", keyword)
		params.Set("num", strconv.Itoa(offset+limit))
	default:
		return nil, errors.New("unsupported search platform")
	}
	root, err := c.get(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}
	obj := root
	var raw []any
	found := false
	for depth := 0; depth < 5; depth++ {
		if err := c.businessError(obj); err != nil {
			return nil, err
		}
		for _, name := range []string{"songs", "list", "tracks"} {
			if value, exists := obj[name]; exists {
				raw, found = value.([]any)
				if !found {
					return nil, errors.New("ChKSz search list is invalid")
				}
				break
			}
		}
		if found {
			break
		}
		if value, ok := obj["result"].(map[string]any); ok {
			obj = value
			continue
		}
		if value, ok := obj["data"].(map[string]any); ok {
			obj = value
			continue
		}
		return nil, errors.New("ChKSz search response has no recognized list")
	}
	if !found {
		return nil, errors.New("ChKSz search response nesting exceeds limit")
	}
	tracks := make([]model.Track, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("ChKSz search item is not an object")
		}
		id := textField(obj, "id", "songId", "song_id")
		if source == "qq" {
			id = textField(obj, "mid", "songmid")
		}
		if !remoteIDPattern.MatchString(id) || (source == "netease" && !numericID(id)) {
			return nil, errors.New("ChKSz search item has an invalid ID")
		}
		track := model.Track{ID: id, Name: textField(obj, "name", "title", "songname"), Hash: textField(obj, "hash", "FileHash", "file_hash")}
		if value, ok := first(obj, "artists", "ar", "singer", "artist"); ok {
			track.Artists = artists(value)
		}
		if value, ok := first(obj, "album", "al"); ok {
			if str, ok := value.(string); ok {
				track.Album = str
			} else if album, ok := value.(map[string]any); ok {
				track.Album = textField(album, "name", "title")
			}
		}
		if value, ok := first(obj, "duration", "interval", "dt"); ok {
			if duration, valid := integer(value); valid && duration >= 0 && duration < 1000000000 {
				if source == "netease" {
					duration /= 1000
				}
				track.Duration = int(duration)
			}
		}
		tracks = append(tracks, track)
	}
	if source != "netease" {
		if offset >= len(tracks) {
			return []model.Track{}, nil
		}
		tracks = tracks[offset:]
	}
	if len(tracks) > limit {
		tracks = tracks[:limit]
	}
	return tracks, nil
}
