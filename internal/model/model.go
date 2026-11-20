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
	PlaylistID string
	Platform   string
	Quality    string
	Directory  string
}
