package qq

import (
	"context"
	"errors"
	"html"
	"strconv"

	"meloport/internal/model"
	"meloport/internal/platform"
)

func (c *Client) playlistRequest(ctx context.Context, module, method string, params map[string]any) (map[string]any, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}
	root, err := c.post(ctx, "/cgi-bin/musicu.fcg", map[string]any{
		"comm":     map[string]any{"cv": 1602, "ct": 20},
		"playlist": map[string]any{"module": module, "method": method, "param": params},
	}, "", false)
	if err != nil {
		return nil, err
	}
	return responseData(root, "playlist")
}

func (c *Client) PlaylistCatalog(ctx context.Context) (model.PlaylistCatalog, error) {
	data, err := c.playlistRequest(ctx, "playlist.PlaylistAllCategoriesServer", "get_all_categories", map[string]any{"qq": ""})
	if err != nil {
		return model.PlaylistCatalog{}, err
	}
	groups, ok := data["v_group"].([]any)
	if !ok || len(groups) > 50 {
		return model.PlaylistCatalog{}, errors.New("QQ playlist categories are invalid")
	}
	catalog := model.PlaylistCatalog{Categories: []model.PlaylistCategory{}, Sorts: []model.PlaylistSort{{ID: "recommend", Name: "推荐"}, {ID: "hot", Name: "最热"}, {ID: "new", Name: "最新"}}, CategorySorts: []model.PlaylistSort{{ID: "default", Name: "默认排序"}}}
	seen, total := map[string]bool{}, 0
	for _, raw := range groups {
		group := platform.Object(raw)
		id, name := platform.Text(group, "group_id"), html.UnescapeString(platform.Text(group, "group_name"))
		tags, ok := group["v_item"].([]any)
		if !ok || id == "" || name == "" || total+len(tags) > platform.MaxPlaylistTags {
			return model.PlaylistCatalog{}, errors.New("QQ playlist category has invalid tags")
		}
		total += len(tags)
		category := model.PlaylistCategory{ID: id, Name: name, Tags: []model.PlaylistTag{}}
		for _, value := range tags {
			item := platform.Object(value)
			id, name := platform.Text(item, "id"), html.UnescapeString(platform.Text(item, "name"))
			if !numericID.MatchString(id) || name == "" || seen[id] {
				return model.PlaylistCatalog{}, errors.New("QQ playlist category contains an invalid tag")
			}
			seen[id] = true
			category.Tags = append(category.Tags, model.PlaylistTag{ID: id, Name: name, Hot: category.Name == "热门"})
		}
		catalog.Categories = append(catalog.Categories, category)
	}
	return catalog, nil
}

func (c *Client) Playlists(ctx context.Context, category, order string, page int) (model.PlaylistPage, error) {
	if !platform.ValidPlaylistQuery(category, page) || (category != "" && (!numericID.MatchString(category) || order != "default")) || (category == "" && order != "recommend" && order != "hot" && order != "new") {
		return model.PlaylistPage{}, errors.New("invalid QQ playlist category, sort or page")
	}
	result := model.PlaylistPage{Items: []model.PlaylistSummary{}, Page: page, Limit: platform.PlaylistPageSize, Total: -1}
	var rows []any
	if category == "" && order == "recommend" {
		data, err := c.playlistRequest(ctx, "music.playlist.PlaylistSquare", "GetRecommendWhole", map[string]any{"IsReqFeed": true, "FeedReq": map[string]any{"From": (page - 1) * platform.PlaylistPageSize, "Size": platform.PlaylistPageSize}})
		if err != nil {
			return model.PlaylistPage{}, err
		}
		feed := platform.Object(data["FeedRsp"])
		list, exists := feed["List"]
		var ok bool
		rows, ok = list.([]any)
		more, moreOK := feed["HasMore"].(bool)
		if !exists || (!ok && list != nil) || !moreOK {
			return model.PlaylistPage{}, errors.New("QQ recommended playlist directory is invalid")
		}
		result.HasMore = more
		for i, raw := range rows {
			rows[i] = platform.Object(raw)["Playlist"]
		}
	} else {
		id := category
		if id == "" {
			id = "3"
			if order == "new" {
				id = "2"
			}
		}
		categoryID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return model.PlaylistPage{}, errors.New("QQ playlist category ID exceeds the native integer range")
		}
		data, err := c.playlistRequest(ctx, "playlist.PlayListCategoryServer", "get_category_content", map[string]any{"titleid": categoryID, "caller": "0", "category_id": categoryID, "size": platform.PlaylistPageSize, "page": page - 1, "use_page": 1})
		if err != nil {
			return model.PlaylistPage{}, err
		}
		content := platform.Object(data["content"])
		rows, _ = content["v_item"].([]any)
		total, ok := platform.Integer(content["total_cnt"])
		if rows == nil || !ok || total < 0 || total > 1000000 {
			return model.PlaylistPage{}, errors.New("QQ playlist directory is invalid")
		}
		result.Total, result.HasMore = int(total), int(total) > (page-1)*platform.PlaylistPageSize+len(rows)
	}
	if len(rows) > platform.PlaylistPageSize {
		return model.PlaylistPage{}, errors.New("QQ playlist page exceeds the requested page size")
	}
	for _, raw := range rows {
		item := platform.Object(platform.Object(raw)["basic"])
		id := platform.Text(item, "tid")
		if !numericID.MatchString(id) {
			return model.PlaylistPage{}, errors.New("QQ playlist directory has an invalid ID")
		}
		entry := model.PlaylistSummary{ID: id, Name: html.UnescapeString(platform.Text(item, "title")), Author: html.UnescapeString(platform.Text(platform.Object(item["creator"]), "nick")), CoverURL: model.CanonicalPlaylistCoverURL(platform.Text(platform.Object(item["cover"]), "medium_url", "default_url")), TrackCount: platform.PlaylistCount(item, "song_cnt"), PlayCount: platform.PlaylistPlayCount(item, "play_cnt")}
		for _, tag := range platform.Slice(item["v_tag"]) {
			if name := html.UnescapeString(platform.Text(platform.Object(tag), "name")); name != "" {
				entry.Tags = append(entry.Tags, name)
			}
		}
		result.Items = append(result.Items, entry)
	}
	var err error
	result.Items, err = platform.NormalizePlaylistSummaries(result.Items)
	if err != nil {
		return model.PlaylistPage{}, err
	}
	return result, nil
}
