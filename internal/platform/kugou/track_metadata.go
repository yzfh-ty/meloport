package kugou

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"meloport/internal/model"
	"meloport/internal/platform"
)

func (c *Client) TrackMetadata(ctx context.Context, track model.Track) (model.Track, error) {
	var hash, audioID string
	switch {
	case hashPattern.MatchString(track.ID):
		hash = strings.ToLower(track.ID)
	case digits.MatchString(track.ID):
		audioID = track.ID
	default:
		return model.Track{}, errors.New("Kugou song details require a hash or audio ID")
	}
	item := map[string]string{"hash": hash}
	if audioID != "" {
		item = map[string]string{"audio_id": audioID}
	}
	rows, err := c.albumAudioRows(ctx, []map[string]string{item})
	if err != nil {
		return model.Track{}, err
	}
	var details model.Track
	for _, row := range rows {
		if tuple, ok := row.([]any); ok {
			if len(tuple) == 0 {
				continue
			}
			row = tuple[0]
		}
		candidate := platform.Object(row)
		audio := platform.Object(candidate["audio_info"])
		if !matchesAlbumAudio(audio, hash, audioID) {
			continue
		}
		if details.ID != "" {
			return model.Track{}, errors.New("Kugou returned ambiguous song details")
		}
		details, err = detailTrack(candidate)
		if err != nil {
			return model.Track{}, err
		}
		if hash != "" {
			details.Hash = hash
		}
		details.ID = track.ID
	}
	if details.ID == "" {
		return model.Track{}, errors.New("Kugou song details do not match the tracked audio")
	}
	return details, nil
}

func matchesAlbumAudio(audio map[string]any, hash, audioID string) bool {
	if audioID != "" {
		return platform.Text(audio, "audio_id") == audioID
	}
	for _, field := range []string{"hash", "hash_320", "hash_flac", "hash_high"} {
		if strings.EqualFold(platform.Text(audio, field), hash) {
			return true
		}
	}
	return false
}

func (c *Client) albumAudioRows(ctx context.Context, items []map[string]string) ([]any, error) {
	payload, err := json.Marshal(map[string]any{
		"area_code": "1", "show_privilege": 1, "show_album_info": "1", "is_publish": "",
		"appid": 1005, "clientver": 11451, "mid": "1", "dfid": "-",
		"clienttime": time.Now().UnixMilli(), "key": androidSigningMaterial,
		"fields": "album_info,author_name,audio_info,ori_audio_name,base,songname,classification,img,album_img",
		"data":   items,
	})
	if err != nil {
		return nil, errors.New("cannot encode Kugou song detail request")
	}
	value, err := c.request(ctx, http.MethodPost, "https://gateway.kugou.com", "/v3/album_audio/audio", nil, payload, false, false)
	if err != nil {
		return nil, err
	}
	rows, ok := value.([]any)
	if !ok {
		return nil, errors.New("Kugou song detail response is missing")
	}
	return rows, nil
}
