package kugou

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"meloport/internal/model"
	"meloport/internal/platform"
)

func (c *Client) Lyrics(ctx context.Context, track model.Track) (string, error) {
	hash := track.Hash
	if hash == "" {
		hash = track.ID
	}
	if !hashPattern.MatchString(hash) {
		return "", nil // A numeric audio ID is insufficient for this exact-match lookup.
	}
	length := 0
	if track.Duration > 0 && track.Duration <= 24*60*60 {
		length = track.Duration * 1000
	}
	query := url.Values{
		"ver": {"1"}, "man": {"yes"}, "client": {"pc"}, "hash": {strings.ToUpper(hash)},
		"timelength": {strconv.Itoa(length)}, "lrctxt": {"1"},
	}
	root, err := c.lyricRequest(ctx, "/search", query)
	if err != nil {
		return "", err
	}
	status, ok := platform.Integer(root["status"])
	candidates := platform.Slice(root["candidates"])
	// Hash-only lookups for tracks with no matches may return an empty list
	// with either 200 or 400. Other business failures should be reported.
	if ok && status == 400 && candidates != nil && len(candidates) == 0 {
		return "", nil
	}
	if !ok || status != 200 || candidates == nil {
		return "", errors.New("Kugou returned an invalid lyric candidate response")
	}
	for _, raw := range candidates {
		candidate := platform.Object(raw)
		// The native service can include other versions of a song in a hash
		// search. Its first hit layer matches the requested audio identity.
		layer, _ := platform.Integer(candidate["hitlayer"])
		if layer != 1 {
			continue
		}
		id := platform.Text(candidate, "id")
		accessKey, _ := candidate["accesskey"].(string)
		if !digits.MatchString(id) || accessKey == "" || len(accessKey) > 1024 {
			return "", errors.New("Kugou returned an invalid lyric candidate")
		}
		return c.downloadLyrics(ctx, id, accessKey)
	}
	return "", nil
}

func (c *Client) downloadLyrics(ctx context.Context, id, accessKey string) (string, error) {
	query := url.Values{"ver": {"1"}, "client": {"pc"}, "id": {id}, "accesskey": {accessKey}, "fmt": {"lrc"}, "charset": {"utf8"}}
	root, err := c.lyricRequest(ctx, "/download", query)
	if err != nil {
		return "", err
	}
	status, _ := platform.Integer(root["status"])
	if status != 200 || root["fmt"] != "lrc" || (root["id"] != nil && platform.Text(root, "id") != id) {
		return "", errors.New("Kugou did not return the requested LRC lyrics")
	}
	if value, present := root["error_code"]; present {
		code, ok := platform.Integer(value)
		if !ok || code != 0 {
			return "", errors.New("Kugou rejected the lyric download")
		}
	}
	content, ok := root["content"].(string)
	if !ok {
		return "", errors.New("Kugou returned invalid lyric content")
	}
	return platform.DecodeLyricsBase64(content)
}

func (c *Client) lyricRequest(ctx context.Context, path string, query url.Values) (map[string]any, error) {
	headers := http.Header{"User-Agent": {"KuGou2012-9020-ExpandSearchManager"}}
	headers.Set("KG-RC", "1")
	headers.Set("KG-THash", "expand_search_manager.cpp:852736169:451")
	return c.http.JSON(ctx, http.MethodGet, c.endpoint("https://lyrics.kugou.com", path)+"?"+query.Encode(), nil, headers)
}
