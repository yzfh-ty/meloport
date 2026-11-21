package netease

import (
	"context"
	"errors"

	"meloport/internal/model"
)

func (c *Client) TrackMetadata(ctx context.Context, track model.Track) (model.Track, error) {
	if !validID(track.ID) {
		return model.Track{}, errors.New("NetEase song details require a valid song ID")
	}
	tracks, err := c.songDetails(ctx, []string{track.ID})
	if err != nil {
		return model.Track{}, err
	}
	return tracks[0], nil
}
