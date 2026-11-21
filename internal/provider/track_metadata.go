package provider

import (
	"context"
	"errors"
	"fmt"

	"meloport/internal/model"
)

// TrackMetadataSource reads metadata for a song's exact native identity.
type TrackMetadataSource interface {
	TrackMetadata(context.Context, model.Track) (model.Track, error)
}

// TrackMetadata refreshes the metadata of a tracked song from its platform.
func (m *Manager) TrackMetadata(ctx context.Context, source string, track model.Track) (model.Track, error) {
	if err := ctx.Err(); err != nil {
		return model.Track{}, err
	}
	reader, ok := m.sources[source].(TrackMetadataSource)
	if !ok {
		return model.Track{}, errors.New("native song details are unavailable for this platform")
	}
	details, err := reader.TrackMetadata(ctx, track)
	if err != nil {
		return model.Track{}, fmt.Errorf("%s native song details: %w", source, err)
	}
	if details.ID == "" || details.ID != track.ID {
		return model.Track{}, errors.New("native song details do not match the tracked song")
	}
	details.Platform = source
	return details, nil
}
