package provider

import (
	"context"
	"fmt"

	"meloport/internal/model"
)

// LyricsSource optionally provides original lyrics for a native song identity.
// A successful empty response indicates that no lyrics are available.
type LyricsSource interface {
	Lyrics(context.Context, model.Track) (string, error)
}

func (m *Manager) Lyrics(ctx context.Context, source string, track model.Track) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	reader, ok := m.sources[source].(LyricsSource)
	if !ok {
		return "", nil
	}
	lyrics, err := reader.Lyrics(ctx, track)
	if err != nil {
		return "", fmt.Errorf("%s native lyrics failed: %w", source, err)
	}
	return lyrics, nil
}
