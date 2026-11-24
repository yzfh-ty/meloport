package app

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/platform"
)

type playlistCatalogCache struct {
	catalog   model.PlaylistCatalog
	fetchedAt time.Time
}

func discoveryPlatform(value string) bool {
	return value == "netease" || value == "qq" || value == "kugou"
}

func (c *webController) nativePlaylistCatalog(ctx context.Context, source string, force bool) (playlistCatalogCache, error) {
	if err := ctx.Err(); err != nil {
		return playlistCatalogCache{}, err
	}
	if !force {
		c.playlistMu.Lock()
		cached, ok := c.playlistCatalogs[source]
		c.playlistMu.Unlock()
		if ok && time.Since(cached.fetchedAt) < 5*time.Minute {
			return cached, nil
		}
	}
	catalog, err := c.metadata.PlaylistCatalog(ctx, source)
	if err != nil {
		return playlistCatalogCache{}, err
	}
	if err := ctx.Err(); err != nil {
		return playlistCatalogCache{}, err
	}
	if catalog.Categories == nil {
		catalog.Categories = []model.PlaylistCategory{}
	}
	if len(catalog.Sorts) == 0 {
		return playlistCatalogCache{}, errors.New("native playlist catalog has no supported sort")
	}
	cached := playlistCatalogCache{catalog: catalog, fetchedAt: time.Now()}
	c.playlistMu.Lock()
	c.playlistCatalogs[source] = cached
	c.playlistMu.Unlock()
	return cached, nil
}

func (c *webController) playlistCatalogHandler(w http.ResponseWriter, r *http.Request) {
	if c.metadata == nil {
		webError(w, http.StatusServiceUnavailable, "metadata readers are unavailable")
		return
	}
	source := r.URL.Query().Get("platform")
	if !discoveryPlatform(source) {
		webError(w, http.StatusBadRequest, "platform must be netease, qq or kugou")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	result, err := c.nativePlaylistCatalog(ctx, source, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		webError(w, http.StatusBadGateway, err.Error())
		return
	}
	webJSON(w, http.StatusOK, map[string]any{"platform": source, "catalog": result.catalog, "fetched_at": result.fetchedAt.Format(time.RFC3339)})
}

func (c *webController) playlistsHandler(w http.ResponseWriter, r *http.Request) {
	if c.metadata == nil {
		webError(w, http.StatusServiceUnavailable, "metadata readers are unavailable")
		return
	}
	q := r.URL.Query()
	source, category, order := q.Get("platform"), q.Get("category"), q.Get("sort")
	page := 1
	if raw := q.Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil {
			webError(w, http.StatusBadRequest, "invalid playlist page")
			return
		}
	}
	if !discoveryPlatform(source) || !platform.ValidPlaylistQuery(category, page) || len(order) > 32 {
		webError(w, http.StatusBadRequest, "provide a supported platform, category and page 1-100")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	cached, err := c.nativePlaylistCatalog(ctx, source, false)
	if err != nil {
		webError(w, http.StatusBadGateway, err.Error())
		return
	}
	sorts := cached.catalog.Sorts
	if category != "" {
		found := false
		for _, group := range cached.catalog.Categories {
			for _, tag := range group.Tags {
				if tag.ID == category {
					found = true
				}
			}
		}
		if !found {
			webError(w, http.StatusBadRequest, "unknown native playlist category")
			return
		}
		if len(cached.catalog.CategorySorts) > 0 {
			sorts = cached.catalog.CategorySorts
		}
	}
	if order == "" {
		order = sorts[0].ID
	}
	validSort := false
	for _, candidate := range sorts {
		if order == candidate.ID {
			validSort = true
		}
	}
	if !validSort {
		webError(w, http.StatusBadRequest, "unsupported sort for this playlist category")
		return
	}
	result, err := c.metadata.Playlists(ctx, source, category, order, page)
	if err != nil {
		webError(w, http.StatusBadGateway, err.Error())
		return
	}
	if result.Items == nil {
		result.Items = []model.PlaylistSummary{}
	}
	webJSON(w, http.StatusOK, map[string]any{"platform": source, "category": category, "sort": order, "playlists": result, "fetched_at": time.Now().Format(time.RFC3339)})
}

func (c *webController) playlistDetail(w http.ResponseWriter, r *http.Request) {
	if c.metadata == nil {
		webError(w, http.StatusServiceUnavailable, "metadata readers are unavailable")
		return
	}
	source, id := r.PathValue("platform"), r.PathValue("id")
	if !discoveryPlatform(source) {
		webError(w, http.StatusBadRequest, "platform must be netease, qq or kugou")
		return
	}
	normalized, err := config.ParsePlaylistFor(source, id)
	if err != nil {
		webError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	playlist, err := c.metadata.Playlist(ctx, source, normalized)
	if err != nil {
		webError(w, http.StatusBadGateway, err.Error())
		return
	}
	webJSON(w, http.StatusOK, playlist)
}
