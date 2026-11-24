package kugou

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"meloport/internal/model"
	"meloport/internal/platform"
)

var currentCollectionID = regexp.MustCompile(`^collection_[1-9][0-9]*_[0-9]+_[0-9]+_[0-9]+$`)

// currentCollection reads the current public Android collection protocol.
// The first page includes its identity and list version; later pages expose
// only the count. Verify the version again after multi-page reads rather than
// accepting a partial or changed playlist.
func (c *Client) currentCollection(ctx context.Context, identity, gcid string) (model.Playlist, error) {
	var members []any
	name, version, total := "", "", -1
	seen := map[string]bool{}
	for page := 1; page <= maxPages; page++ {
		offset := (page - 1) * globalPageSize
		query := url.Values{"area_code": {"1"}, "begin_idx": {strconv.Itoa(offset)}, "plat": {"1"}, "type": {"1"}, "mode": {"1"}, "personal_switch": {"1"}, "pagesize": {strconv.Itoa(globalPageSize)}, "global_collection_id": {gcid}}
		value, err := c.androidMetadataRequest(ctx, http.MethodGet, "/pubsongs/v2/get_other_list_file_nofilt", query, nil, "")
		if err != nil {
			return model.Playlist{}, err
		}
		data := platform.Object(value)
		info := platform.Object(data["list_info"])
		if info == nil {
			return model.Playlist{}, errors.New("Kugou collection metadata is missing")
		}
		count, err := countField(data, "count")
		if err != nil {
			return model.Playlist{}, err
		}
		begin, beginOK := platform.Integer(data["begin_idx"])
		size, sizeOK := platform.Integer(data["pagesize"])
		if !beginOK || begin != int64(offset) || !sizeOK || size != globalPageSize {
			return model.Playlist{}, errors.New("Kugou collection returned a different page cursor")
		}
		if page == 1 || len(info) > 0 {
			currentName, currentVersion, infoCount, err := currentCollectionState(info, gcid)
			if err != nil {
				return model.Playlist{}, err
			}
			if infoCount != count {
				return model.Playlist{}, errors.New("Kugou collection count does not match its metadata")
			}
			if page == 1 {
				name, version, total = currentName, currentVersion, count
			} else if name != currentName || version != currentVersion {
				return model.Playlist{}, errors.New("Kugou collection changed during pagination")
			}
		}
		if total != count {
			return model.Playlist{}, errors.New("Kugou collection changed during pagination")
		}
		list, ok := data["songs"].([]any)
		if !ok {
			return model.Playlist{}, errors.New("Kugou collection member list is missing")
		}
		if err := appendPage(&members, list, total, globalPageSize, seen); err != nil {
			return model.Playlist{}, err
		}
		if len(members) == total {
			if page > 1 {
				value, err := c.discoveryRequest(ctx, "/v3/get_list_info", map[string]any{"data": []any{map[string]any{"global_collection_id": gcid}}, "userid": 0, "token": ""}, "pubsongs.kugou.com")
				if err != nil {
					return model.Playlist{}, err
				}
				infos, ok := value.([]any)
				if !ok || len(infos) != 1 {
					return model.Playlist{}, errors.New("Kugou collection verification returned ambiguous metadata")
				}
				currentName, currentVersion, count, err := currentCollectionState(platform.Object(infos[0]), gcid)
				if err != nil {
					return model.Playlist{}, err
				}
				if currentName != name || currentVersion != version || count != total {
					return model.Playlist{}, errors.New("Kugou collection changed during pagination")
				}
			}
			return c.buildPlaylist(ctx, identity, name, members, total)
		}
	}
	return model.Playlist{}, errors.New("Kugou collection exceeds the bounded pagination budget")
}

func currentCollectionState(info map[string]any, gcid string) (string, string, int, error) {
	if platform.Text(info, "global_collection_id") != gcid {
		return "", "", 0, errors.New("Kugou returned a different collection identity")
	}
	code, ok := platform.Integer(info["code"])
	if !ok || code != 1 {
		return "", "", 0, errors.New("Kugou public collection is unavailable")
	}
	count, err := countField(info, "count")
	if err != nil {
		return "", "", 0, err
	}
	name, version := platform.Text(info, "name"), platform.Text(info, "list_ver")
	if name == "" || version == "" {
		return "", "", 0, errors.New("Kugou collection has no name or version")
	}
	return name, version, count, nil
}
