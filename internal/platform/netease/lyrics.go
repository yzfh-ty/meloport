package netease

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"meloport/internal/model"
	"meloport/internal/platform"
)

func (c *Client) Lyrics(ctx context.Context, track model.Track) (string, error) {
	if !validID(track.ID) {
		return "", errors.New("NetEase lyrics require a valid song ID")
	}
	query := url.Values{"id": {track.ID}, "lv": {"1"}}
	root, err := c.http.JSON(ctx, http.MethodGet, c.base+"/api/song/lyric?"+query.Encode(), nil, c.headers())
	if err != nil {
		return "", err
	}
	if err := responseOK(root); err != nil {
		return "", err
	}
	if root["nolyric"] == true || root["uncollected"] == true || root["lrc"] == nil {
		return "", nil
	}
	lrc := platform.Object(root["lrc"])
	if lrc == nil {
		return "", errors.New("NetEase returned an invalid lyric object")
	}
	if lrc["lyric"] == nil {
		return "", nil
	}
	lyrics, ok := lrc["lyric"].(string)
	if !ok {
		return "", errors.New("NetEase returned invalid lyric content")
	}
	return platform.NormalizeLyrics(lyrics)
}
