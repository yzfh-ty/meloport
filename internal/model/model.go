// Package model contains the provider-independent data used by synchronization.
package model

type Track struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Artists  []string `json:"artists"`
	Album    string   `json:"album"`
	Hash     string   `json:"hash,omitempty"`
	Duration int      `json:"duration,omitempty"`
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
	URL    string
	Format string
	Size   int64
}

type DownloadResult struct {
	Path   string
	Size   int64
	SHA256 string
}

// Task is the fully validated, effective configuration for one subscription.
type Task struct {
	ID         string
	SourceType string
	SourceID   string
	// PlaylistID is retained for callers constructing legacy tasks directly.
	// Configuration-backed tasks always populate SourceID as the canonical value.
	PlaylistID string
	Platform   string
	Quality    string
	Directory  string
}
