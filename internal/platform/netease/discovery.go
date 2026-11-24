package netease

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"meloport/internal/model"
	"meloport/internal/platform"
)

func (c *Client) PlaylistCatalog(ctx context.Context) (model.PlaylistCatalog, error) {
	root, err := c.http.JSON(ctx, http.MethodGet, c.base+"/api/playlist/catalogue", nil, c.headers())
	if err != nil {
		return model.PlaylistCatalog{}, err
	}
	if err := responseOK(root); err != nil {
		return model.PlaylistCatalog{}, err
	}
	names, ok := root["categories"].(map[string]any)
	tags, tagsOK := root["sub"].([]any)
	if !ok || !tagsOK || len(names) > 50 || len(tags) > platform.MaxPlaylistTags {
		return model.PlaylistCatalog{}, errors.New("NetEase playlist categories are invalid")
	}
	keys := make([]string, 0, len(names))
	for key := range names {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	catalog := model.PlaylistCatalog{Categories: []model.PlaylistCategory{}, Sorts: []model.PlaylistSort{{ID: "hot", Name: "最热"}, {ID: "new", Name: "最新"}}}
	groups := make(map[string]int, len(keys))
	for _, key := range keys {
		name, ok := names[key].(string)
		if !ok || name == "" {
			return model.PlaylistCatalog{}, errors.New("NetEase playlist category has no name")
		}
		groups[key] = len(catalog.Categories)
		catalog.Categories = append(catalog.Categories, model.PlaylistCategory{ID: key, Name: name, Tags: []model.PlaylistTag{}})
	}
	seen := map[string]bool{}
	for _, raw := range tags {
		item := platform.Object(raw)
		name := platform.Text(item, "name")
		group, exists := groups[platform.Text(item, "category")]
		if !exists || name == "" || seen[name] {
			return model.PlaylistCatalog{}, errors.New("NetEase playlist category contains an invalid tag")
		}
		seen[name] = true
		hot, _ := item["hot"].(bool)
		catalog.Categories[group].Tags = append(catalog.Categories[group].Tags, model.PlaylistTag{ID: name, Name: name, Hot: hot})
	}
	return catalog, nil
}

func (c *Client) Playlists(ctx context.Context, category, order string, page int) (model.PlaylistPage, error) {
	if !platform.ValidPlaylistQuery(category, page) || (order != "hot" && order != "new") {
		return model.PlaylistPage{}, errors.New("invalid NetEase playlist category, sort or page")
	}
	if category == "" {
		category = "全部"
	}
	q := url.Values{"cat": {category}, "order": {order}, "limit": {strconv.Itoa(platform.PlaylistPageSize)}, "offset": {strconv.Itoa((page - 1) * platform.PlaylistPageSize)}}
	root, err := c.http.JSON(ctx, http.MethodGet, c.base+"/api/playlist/list?"+q.Encode(), nil, c.headers())
	if err != nil {
		return model.PlaylistPage{}, err
	}
	if err := responseOK(root); err != nil {
		return model.PlaylistPage{}, err
	}
	raw, ok := root["playlists"].([]any)
	total, totalOK := platform.Integer(root["total"])
	more, moreOK := root["more"].(bool)
	if !ok || !totalOK || total < 0 || total > 1000000 || !moreOK || len(raw) > platform.PlaylistPageSize {
		return model.PlaylistPage{}, errors.New("NetEase playlist directory is invalid")
	}
	result := model.PlaylistPage{Items: []model.PlaylistSummary{}, Page: page, Limit: platform.PlaylistPageSize, Total: int(total), HasMore: more}
	for _, value := range raw {
		item := platform.Object(value)
		id := platform.Text(item, "id")
		if !validID(id) {
			return model.PlaylistPage{}, errors.New("NetEase playlist directory has an invalid ID")
		}
		entry := model.PlaylistSummary{ID: id, Name: platform.Text(item, "name"), Author: platform.Text(platform.Object(item["creator"]), "nickname"), CoverURL: model.CanonicalPlaylistCoverURL(platform.Text(item, "coverImgUrl")), TrackCount: platform.PlaylistCount(item, "trackCount"), PlayCount: platform.PlaylistPlayCount(item, "playCount")}
		for _, tag := range platform.Slice(item["tags"]) {
			if name, ok := tag.(string); ok && name != "" {
				entry.Tags = append(entry.Tags, name)
			}
		}
		result.Items = append(result.Items, entry)
	}
	result.Items, err = platform.NormalizePlaylistSummaries(result.Items)
	if err != nil {
		return model.PlaylistPage{}, err
	}
	return result, nil
}
