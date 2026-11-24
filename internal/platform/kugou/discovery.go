package kugou

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"meloport/internal/model"
	"meloport/internal/platform"
)

const discoveryClientVersion = "20489"

// The current Android discovery protocol is independent from the retired
// www2.kugou web catalog. These are public protocol values, not account keys.
func (c *Client) discoveryRequest(ctx context.Context, path string, body map[string]any, router string) (any, error) {
	return c.androidMetadataRequest(ctx, http.MethodPost, path, nil, body, router)
}

func (c *Client) androidMetadataRequest(ctx context.Context, method, path string, query url.Values, body map[string]any, router string) (any, error) {
	now := strconv.FormatInt(time.Now().Unix(), 10)
	mid := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if path == "/v2/special_recommend" {
		body["appid"], body["clientver"], body["clienttime"], body["mid"] = 1005, 20489, now, mid
		sum := md5.Sum([]byte("1005" + androidSigningMaterial + discoveryClientVersion + now))
		body["key"] = hex.EncodeToString(sum[:])
	}
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	q := url.Values{"dfid": {"-"}, "mid": {mid}, "uuid": {"-"}, "appid": {"1005"}, "clientver": {discoveryClientVersion}, "clienttime": {now}}
	for key, values := range query {
		q[key] = append([]string(nil), values...)
	}
	q.Set("signature", signature(q.Encode(), androidSigningMaterial, string(payload)))
	h := c.headers()
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "Android15-1070-11083-46-0-DiscoveryDRADProtocol-wifi")
	h.Set("dfid", "-")
	h.Set("mid", mid)
	h.Set("clienttime", now)
	h.Set("kg-rc", "1")
	h.Set("kg-thash", "5d816a0")
	h.Set("kg-rec", "1")
	h.Set("kg-rf", "B9EDA08A64250DEFFBCADDEE00F8F25F")
	if router != "" {
		h.Set("X-Router", router)
	}
	uri := c.endpoint("https://gateway.kugou.com", path)
	if parsed, err := url.Parse(uri); err == nil && parsed.Scheme != "https" {
		h.Del("Cookie")
	}
	root, err := c.http.JSON(ctx, method, uri+"?"+q.Encode(), payload, h)
	if err != nil {
		return nil, err
	}
	return unwrap(root)
}

func (c *Client) PlaylistCatalog(ctx context.Context) (model.PlaylistCatalog, error) {
	data, err := c.discoveryRequest(ctx, "/pubsongs/v1/get_tags_by_type", map[string]any{"tag_type": "collection", "tag_id": 0, "source": 3}, "")
	if err != nil {
		return model.PlaylistCatalog{}, err
	}
	groups, ok := data.([]any)
	if !ok || len(groups) > 50 {
		return model.PlaylistCatalog{}, errors.New("Kugou playlist categories are invalid")
	}
	catalog := model.PlaylistCatalog{Categories: []model.PlaylistCategory{}, Sorts: []model.PlaylistSort{{ID: "recommend", Name: "推荐"}}}
	seen, total := map[string]bool{}, 0
	for _, raw := range groups {
		item := platform.Object(raw)
		id, name := platform.Text(item, "tag_id"), html.UnescapeString(platform.Text(item, "tag_name"))
		tags, ok := item["son"].([]any)
		if !ok || !digits.MatchString(id) || name == "" || total+len(tags) > platform.MaxPlaylistTags {
			return model.PlaylistCatalog{}, errors.New("Kugou playlist category has invalid tags")
		}
		total += len(tags)
		category := model.PlaylistCategory{ID: id, Name: name, Tags: []model.PlaylistTag{}}
		for _, raw := range tags {
			tag := platform.Object(raw)
			id, name := platform.Text(tag, "tag_id"), html.UnescapeString(platform.Text(tag, "tag_name"))
			if !digits.MatchString(id) || name == "" || seen[id] {
				return model.PlaylistCatalog{}, errors.New("Kugou playlist category contains an invalid tag")
			}
			seen[id] = true
			category.Tags = append(category.Tags, model.PlaylistTag{ID: id, Name: name})
		}
		catalog.Categories = append(catalog.Categories, category)
	}
	return catalog, nil
}

func (c *Client) Playlists(ctx context.Context, category, order string, page int) (model.PlaylistPage, error) {
	if !platform.ValidPlaylistQuery(category, page) || (category != "" && !digits.MatchString(category)) || order != "recommend" {
		return model.PlaylistPage{}, errors.New("invalid Kugou playlist category, sort or page")
	}
	categoryID := int64(0)
	if category != "" {
		var err error
		categoryID, err = strconv.ParseInt(category, 10, 64)
		if err != nil {
			return model.PlaylistPage{}, errors.New("Kugou playlist category ID exceeds the native integer range")
		}
	}
	data, err := c.discoveryRequest(ctx, "/v2/special_recommend", map[string]any{
		"platform": "android", "userid": 0, "module_id": 1, "page": page, "pagesize": platform.PlaylistPageSize,
		"special_recommend": map[string]any{"withtag": 1, "withsong": 0, "sort": 1, "ugc": 1, "is_selected": 0, "withrecommend": 0, "area_code": 1, "categoryid": categoryID},
		"req_multi":         1, "retrun_min": 5, "return_special_falg": 1,
	}, "specialrec.service.kugou.com")
	if err != nil {
		return model.PlaylistPage{}, err
	}
	object := platform.Object(data)
	rows, ok := object["special_list"].([]any)
	more, moreOK := platform.Integer(object["has_next"])
	if !ok || !moreOK || (more != 0 && more != 1) || len(rows) > 2*platform.PlaylistPageSize {
		return model.PlaylistPage{}, errors.New("Kugou playlist directory is invalid")
	}
	result := model.PlaylistPage{Items: []model.PlaylistSummary{}, Page: page, Limit: platform.PlaylistPageSize, Total: -1, HasMore: more == 1}
	for _, raw := range rows {
		item := platform.Object(raw)
		// Global collection IDs identify current user playlists unambiguously;
		// numeric special IDs are a different family from numeric share codes.
		id := ""
		if global := platform.Text(item, "global_collection_id"); global != "" && token.MatchString(global) {
			id = "https://m3ws.kugou.com/share/index.php?" + url.Values{"global_collection_id": {global}}.Encode()
		} else if special := platform.Text(item, "specialid"); digits.MatchString(special) {
			id = "id_" + special
		}
		if id == "" {
			return model.PlaylistPage{}, errors.New("Kugou playlist directory has no supported playlist identity")
		}
		entry := model.PlaylistSummary{ID: id, Name: html.UnescapeString(platform.Text(item, "specialname")), Author: html.UnescapeString(platform.Text(item, "nickname")), CoverURL: model.CanonicalPlaylistCoverURL(platform.Text(item, "flexible_cover", "imgurl")), TrackCount: platform.PlaylistCount(item, "songcount"), PlayCount: platform.PlaylistPlayCount(item, "play_count")}
		// withsong=0 makes this directory emit a zero placeholder even for
		// nonempty playlists. Only positive directory counts are informative;
		// the detail endpoint supplies the authoritative count, including zero.
		if entry.TrackCount != nil && *entry.TrackCount == 0 {
			entry.TrackCount = nil
		}
		for _, raw := range platform.Slice(item["tags"]) {
			if name := html.UnescapeString(platform.Text(platform.Object(raw), "tag_name")); name != "" {
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
