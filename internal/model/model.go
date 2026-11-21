// Package model contains the provider-independent data used by synchronization.
package model

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Track struct {
	ID           string   `json:"id"`
	Platform     string   `json:"-"` // Runtime namespace for native lookups on this ID.
	Name         string   `json:"name"`
	Artists      []string `json:"artists"`
	Album        string   `json:"album"`
	AlbumArtists []string `json:"album_artists,omitempty"`
	TrackNumber  int      `json:"track_number,omitempty"`
	DiscNumber   int      `json:"disc_number,omitempty"`
	Year         string   `json:"year,omitempty"`
	CoverURL     string   `json:"cover_url,omitempty"`
	Hash         string   `json:"hash,omitempty"`
	Duration     int      `json:"duration,omitempty"`
}

type Playlist struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Tracks        []Track `json:"tracks"`
	ReportedCount int     `json:"reported_count"`
	FetchedVia    string  `json:"fetched_via,omitempty"`
}

// Ranking is a native platform chart that can be subscribed to as a dynamic
// source. The ID is platform-specific and must not be treated as a playlist
// ID by callers.
type Ranking struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Category        string `json:"category,omitempty"`
	TrackCount      int    `json:"track_count,omitempty"`
	UpdateFrequency string `json:"update_frequency,omitempty"`
}

type ResolvedTrack struct {
	URL      string
	Format   string
	Size     int64
	Track    Track
	CoverURL string
}

type DownloadResult struct {
	Path         string
	Size         int64
	SHA256       string
	TagsComplete bool
	TagWarning   string
}

// CanonicalCoverURL keeps only public platform-CDN image addresses that can be
// persisted with metadata. Signed URLs, credentials and arbitrary hosts are
// deliberately excluded; a missing cover must never block the audio download.
func CanonicalCoverURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Port() != "" || u.Path == "" || strings.Contains(strings.ToLower(u.Path), "chksz_") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	allowed := host == "y.gtimg.cn" || host == "imge.kugou.com" || host == "imgessl.kugou.com" || host == "music.126.net" || strings.HasSuffix(host, ".music.126.net") || host == "kgimg.com" || strings.HasSuffix(host, ".kgimg.com")
	if !allowed {
		return ""
	}
	u.Scheme, u.Host = "https", host
	return u.String()
}

// Task is the fully validated, effective configuration for one subscription.
type Task struct {
	ID         string
	SourceType string
	SourceID   string
	// PlaylistID is retained for callers constructing legacy tasks directly.
	// Configuration-backed tasks always populate SourceID as the canonical value.
	PlaylistID    string
	Platform      string
	Quality       string
	AutoDowngrade bool
	Directory     string
}

// CollectionFolder returns a portable, task-stable folder name that retains
// the source chart or playlist title while preventing same-title collisions.
func CollectionFolder(taskID, title string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.TrimSpace(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			extra := utf8.RuneLen(r)
			if separator && b.Len() > 0 {
				extra++
			}
			if b.Len()+extra > 96 {
				break
			}
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			separator = false
			continue
		}
		separator = true
	}
	name := strings.Trim(b.String(), "-.")
	if name == "" {
		name = "collection"
	}
	if taskID == "" {
		return name
	}
	return name + "--" + taskID
}
