package chksz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"meloport/internal/model"
)

// fixturePlaylist is a test-only decoder harness for the HTTP transport,
// credential redaction and rate-limit fixtures. Playlist metadata is never
// requested through ChKSz by the application.
func (c *Client) fixturePlaylist(ctx context.Context, id string) (model.Playlist, error) {
	if !numericID(id) {
		return model.Playlist{}, errors.New("playlist ID must contain only digits")
	}
	root, err := c.get(ctx, "/api/163_playlist", url.Values{"id": {id}})
	if err != nil {
		return model.Playlist{}, err
	}
	obj, envelopes, err := c.unwrapPath(root, "playlist")
	if err != nil {
		return model.Playlist{}, err
	}
	rawTracks, ok := first(obj, "tracks", "songs", "list", "songList")
	if !ok {
		return model.Playlist{}, errors.New("ChKSz playlist response has no recognized track list")
	}
	items, ok := rawTracks.([]any)
	if !ok {
		return model.Playlist{}, errors.New("ChKSz playlist track list must be a JSON array")
	}
	playlist := model.Playlist{
		ID: id, Name: textField(obj, "name", "playlistName", "playlist_name", "title"),
		Tracks: make([]model.Track, 0, len(items)), ReportedCount: len(items),
	}
	if remoteID := textField(obj, "id", "playlistId", "playlist_id"); remoteID != "" {
		if remoteID != id {
			return model.Playlist{}, errors.New("ChKSz returned a different playlist ID")
		}
	}
	// Count metadata can live outside data/playlist. Inspect only the selected
	// wrapper path, but require every recognized count alias on it to agree.
	for _, envelope := range envelopes {
		for _, field := range []string{"trackCount", "track_count", "songCount", "song_count", "total", "count"} {
			count, present := envelope[field]
			if !present {
				continue
			}
			n, valid := integer(count)
			if !valid || n < 0 || int64(int(n)) != n {
				return model.Playlist{}, errors.New("ChKSz playlist contains an invalid track count")
			}
			if n != int64(len(items)) {
				return model.Playlist{}, fmt.Errorf("ChKSz playlist is incomplete or inconsistent: reported %d tracks, received %d", n, len(items))
			}
		}
	}
	for i, raw := range items {
		trackObj, ok := raw.(map[string]any)
		if !ok {
			return model.Playlist{}, fmt.Errorf("ChKSz playlist track %d is not an object", i+1)
		}
		trackID := textField(trackObj, "id", "songId", "song_id")
		if !numericID(trackID) {
			return model.Playlist{}, fmt.Errorf("ChKSz playlist track %d has no valid song ID", i+1)
		}
		track := model.Track{ID: trackID, Name: textField(trackObj, "name", "songName", "song_name", "title")}
		if value, found := first(trackObj, "artists", "ar", "artist", "singer"); found {
			track.Artists = artists(value)
		}
		if value, found := first(trackObj, "album", "al"); found {
			switch album := value.(type) {
			case string:
				track.Album = album
			case map[string]any:
				track.Album = textField(album, "name", "title")
				track.CoverURL = c.coverURL(textField(album, "picUrl"))
			}
		}
		if track.CoverURL == "" {
			track.CoverURL = c.coverURL(textField(trackObj, "cover"))
		}
		playlist.Tracks = append(playlist.Tracks, track)
	}
	for _, envelope := range envelopes {
		if err := validateAuthoritativeIDs(envelope, playlist.Tracks); err != nil {
			return model.Playlist{}, err
		}
	}
	return playlist, nil
}

// Native NetEase responses can include all member IDs but only the first batch
// of embedded song details. Count equality alone also misses wrong-ID batches.
// Duplicates are permitted: the engine synchronizes distinct song identities.
func validateAuthoritativeIDs(obj map[string]any, tracks []model.Track) error {
	actual := make(map[string]struct{}, len(tracks))
	for _, track := range tracks {
		actual[track.ID] = struct{}{}
	}
	for _, field := range []string{"trackIds", "track_ids"} {
		rawIDs, present := obj[field]
		if !present {
			continue
		}
		items, valid := rawIDs.([]any)
		if !valid {
			return errors.New("ChKSz authoritative playlist IDs must be a JSON array")
		}
		expected := make(map[string]struct{}, len(items))
		for i, raw := range items {
			var id string
			switch value := raw.(type) {
			case map[string]any:
				id = textField(value, "id")
			case string:
				id = value
			case json.Number:
				id = value.String()
			}
			if !numericID(id) {
				return fmt.Errorf("ChKSz authoritative playlist ID %d is invalid", i+1)
			}
			expected[id] = struct{}{}
		}
		if len(expected) != len(actual) {
			return fmt.Errorf("ChKSz playlist is incomplete or inconsistent: authoritative IDs contain %d distinct songs, received %d", len(expected), len(actual))
		}
		for id := range expected {
			if _, found := actual[id]; !found {
				return errors.New("ChKSz playlist song details do not match the authoritative playlist IDs")
			}
		}
	}
	return nil
}

func (l *Lazy) fixturePlaylist(ctx context.Context, id string) (model.Playlist, error) {
	c, err := l.get()
	if err != nil {
		return model.Playlist{}, err
	}
	return c.fixturePlaylist(ctx, id)
}
