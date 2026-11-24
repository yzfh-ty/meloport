package platform

import (
	"errors"
	"strings"
	"unicode"

	"meloport/internal/model"
)

const (
	PlaylistPageSize = 30
	MaxPlaylistPage  = 100
	MaxPlaylistTags  = 1000
)

func ValidPlaylistQuery(category string, page int) bool {
	return page >= 1 && page <= MaxPlaylistPage && len([]rune(category)) <= 80 && strings.IndexFunc(category, unicode.IsControl) < 0
}

func PlaylistCount(object map[string]any, fields ...string) *int {
	for _, field := range fields {
		if value, ok := Integer(object[field]); ok && value >= 0 && value <= 1000000 {
			count := int(value)
			return &count
		}
	}
	return nil
}

func PlaylistPlayCount(object map[string]any, fields ...string) *int64 {
	for _, field := range fields {
		if value, ok := Integer(object[field]); ok && value >= 0 {
			return &value
		}
	}
	return nil
}

// NormalizePlaylistSummaries removes repeated promotion entries without
// changing the native page cursor. Playlist song identities are validated by
// the separate native member readers.
func NormalizePlaylistSummaries(items []model.PlaylistSummary) ([]model.PlaylistSummary, error) {
	if len(items) > 2*PlaylistPageSize {
		return nil, errors.New("platform playlist page exceeds the bounded page size")
	}
	seen := make(map[string]bool, len(items))
	unique := items[:0]
	for _, item := range items {
		if item.ID == "" || strings.TrimSpace(item.Name) == "" {
			return nil, errors.New("platform playlist page has an invalid playlist")
		}
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		unique = append(unique, item)
	}
	return unique, nil
}
