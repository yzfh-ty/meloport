package provider

import (
	"context"
	"errors"
	"fmt"

	"meloport/internal/model"
)

// DiscoverySource is optional so extensions implementing Source do not need
// placeholder playlist directories. Discovery always uses native metadata.
type DiscoverySource interface {
	PlaylistCatalog(context.Context) (model.PlaylistCatalog, error)
	Playlists(context.Context, string, string, int) (model.PlaylistPage, error)
}

func (m *Manager) discoverySource(ctx context.Context, source string) (DiscoverySource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader, ok := m.sources[source]
	if !ok {
		return nil, errors.New("unsupported playlist discovery platform")
	}
	directory, ok := reader.(DiscoverySource)
	if !ok {
		return nil, fmt.Errorf("%s platform does not expose native playlist discovery", source)
	}
	return directory, nil
}

func (m *Manager) PlaylistCatalog(ctx context.Context, source string) (model.PlaylistCatalog, error) {
	reader, err := m.discoverySource(ctx, source)
	if err != nil {
		return model.PlaylistCatalog{}, err
	}
	return reader.PlaylistCatalog(ctx)
}

func (m *Manager) Playlists(ctx context.Context, source, category, order string, page int) (model.PlaylistPage, error) {
	reader, err := m.discoverySource(ctx, source)
	if err != nil {
		return model.PlaylistPage{}, err
	}
	return reader.Playlists(ctx, category, order, page)
}
