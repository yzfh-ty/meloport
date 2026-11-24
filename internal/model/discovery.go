package model

import (
	"net/url"
	"strings"
)

// PlaylistCatalog keeps platform-specific categories and sort capabilities.
// CategorySorts overrides Sorts when a tag is selected; nil means the same
// sorting options apply to both the complete directory and individual tags.
type PlaylistCatalog struct {
	Categories    []PlaylistCategory `json:"categories"`
	Sorts         []PlaylistSort     `json:"sorts"`
	CategorySorts []PlaylistSort     `json:"category_sorts,omitempty"`
}

type PlaylistCategory struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	Tags []PlaylistTag `json:"tags"`
}

type PlaylistTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Hot  bool   `json:"hot,omitempty"`
}

type PlaylistSort struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PlaylistSummary describes a discoverable playlist, without fetching songs or
// resolving audio. ID must be accepted by the same platform's playlist reader.
type PlaylistSummary struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Author     string   `json:"author,omitempty"`
	CoverURL   string   `json:"cover_url,omitempty"`
	TrackCount *int     `json:"track_count,omitempty"`
	PlayCount  *int64   `json:"play_count,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

type PlaylistPage struct {
	Items   []PlaylistSummary `json:"items"`
	Page    int               `json:"page"`
	Limit   int               `json:"limit"`
	Total   int               `json:"total"` // -1 when the platform exposes no exact total.
	HasMore bool              `json:"has_more"`
}

// CanonicalPlaylistCoverURL accepts public playlist images. QQ's new image
// host uses an unsigned resize command; reject every other query rather than
// forwarding authentication parameters. Song cover handling remains separate.
func CanonicalPlaylistCoverURL(raw string) string {
	raw = strings.ReplaceAll(raw, "{size}", "300")
	if cover := CanonicalCoverURL(raw); cover != "" {
		return cover
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" || u.Hostname() != "music-file.y.qq.com" || !strings.HasPrefix(u.Path, "/songlist/") || u.Fragment != "" || len(raw) > 2048 {
		return ""
	}
	if u.RawQuery != "" && u.RawQuery != "imageView2/4/w/300/h/300" && u.RawQuery != "imageView2/4/w/600/h/600" {
		return ""
	}
	u.Scheme, u.RawQuery, u.ForceQuery = "https", "imageView2/4/w/300/h/300", false
	return u.String()
}
