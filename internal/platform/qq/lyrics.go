package qq

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"

	"meloport/internal/model"
	"meloport/internal/platform"
)

var legacyLyricTimestamp = regexp.MustCompile(`\[(\d{2,}:\d{2}):(\d{2,3})\]`)

func (c *Client) Lyrics(ctx context.Context, track model.Track) (string, error) {
	if c.initErr != nil {
		return "", c.initErr
	}
	if !midPattern.MatchString(track.ID) {
		return "", errors.New("QQ Music lyrics require a valid song MID")
	}
	query := url.Values{
		"songmid": {track.ID}, "format": {"json"}, "g_tk": {"5381"}, "loginUin": {"0"}, "hostUin": {"0"},
		"inCharset": {"utf8"}, "outCharset": {"utf-8"}, "platform": {"yqq"},
	}
	headers := http.Header{"Referer": {"https://y.qq.com/portal/player.html"}}
	if c.cookie != "" {
		headers.Set("Cookie", c.cookie)
	}
	root, err := c.http.JSON(ctx, http.MethodGet, c.catalogBase+"/lyric/fcgi-bin/fcg_query_lyric_new.fcg?"+query.Encode(), nil, headers)
	if err != nil {
		return "", err
	}
	code, ok := platform.Integer(root["code"])
	if !ok {
		return "", errors.New("QQ Music lyric response has no valid business status")
	}
	if code == 1101 && root["lyric"] == nil {
		return "", nil
	}
	if code != 0 {
		return "", errors.New("QQ Music rejected the lyric request")
	}
	if root["lyric"] == nil {
		return "", nil
	}
	encoded, ok := root["lyric"].(string)
	if !ok {
		return "", errors.New("QQ Music returned invalid lyric content")
	}
	lyrics, err := platform.DecodeLyricsBase64(encoded)
	if err != nil {
		return "", err
	}
	lyrics = html.UnescapeString(lyrics)
	lyrics = legacyLyricTimestamp.ReplaceAllString(lyrics, "[${1}.${2}]")
	return platform.NormalizeLyrics(lyrics)
}
