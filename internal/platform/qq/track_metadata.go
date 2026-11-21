package qq

import (
	"context"
	"errors"

	"meloport/internal/model"
)

func (c *Client) TrackMetadata(ctx context.Context, track model.Track) (model.Track, error) {
	if c.initErr != nil {
		return model.Track{}, c.initErr
	}
	if !midPattern.MatchString(track.ID) {
		return model.Track{}, errors.New("QQ Music song details require a song MID")
	}
	const requestKey = "req_1"
	payload := map[string]any{
		"comm": map[string]int{"ct": 24, "cv": 0},
		requestKey: map[string]any{
			"module": "music.pf_song_detail_svr", "method": "get_song_detail_yqq",
			"param": map[string]any{"song_mid": track.ID, "song_id": 0, "song_type": 0},
		},
	}
	root, err := c.post(ctx, "/cgi-bin/musicu.fcg", payload, "", false)
	if err != nil {
		return model.Track{}, err
	}
	data, err := responseData(root, requestKey)
	if err != nil {
		return model.Track{}, err
	}
	details, err := decodeTrack(data["track_info"])
	if err != nil {
		return model.Track{}, err
	}
	if details.ID != track.ID {
		return model.Track{}, errors.New("QQ Music returned a different song MID")
	}
	return details, nil
}
