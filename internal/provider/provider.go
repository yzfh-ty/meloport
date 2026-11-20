// Package provider selects native metadata before the metered audio gateway.
package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"

	"meloport/internal/model"
)

type Source interface {
	Playlist(context.Context, string) (model.Playlist, error)
	Search(context.Context, string, int, int) ([]model.Track, error)
}

type Gateway interface {
	Playlist(context.Context, string) (model.Playlist, error)
	Search(context.Context, string, string, int, int) ([]model.Track, error)
	ResolvePlatform(context.Context, string, string, string) (model.ResolvedTrack, error)
}

type Manager struct {
	sources  map[string]Source
	api      Gateway
	fallback atomic.Bool
	logger   *slog.Logger
}

func New(sources map[string]Source, api Gateway, fallback bool, logger *slog.Logger) *Manager {
	m := &Manager{sources: sources, api: api, logger: logger}
	m.fallback.Store(fallback)
	return m
}

func (m *Manager) SetFallback(enabled bool) { m.fallback.Store(enabled) }

func (m *Manager) Playlist(ctx context.Context, source, id string) (model.Playlist, error) {
	reader, ok := m.sources[source]
	if !ok {
		return model.Playlist{}, errors.New("unsupported playlist platform")
	}
	playlist, err := reader.Playlist(ctx, id)
	if err == nil {
		playlist.FetchedVia = "native"
		return playlist, nil
	}
	if ctx.Err() != nil {
		return model.Playlist{}, ctx.Err()
	}
	if !m.fallback.Load() || source != "netease" || m.api == nil {
		return model.Playlist{}, fmt.Errorf("%s native playlist failed (no supported fallback): %w", source, err)
	}
	m.logFallback(source, "playlist", err)
	playlist, fallbackErr := m.api.Playlist(ctx, id)
	if fallbackErr != nil {
		return model.Playlist{}, errors.Join(fmt.Errorf("native playlist: %w", err), fmt.Errorf("ChKSz playlist fallback: %w", fallbackErr))
	}
	playlist.FetchedVia = "chksz_fallback"
	return playlist, nil
}

func (m *Manager) Search(ctx context.Context, source, keyword string, limit, offset int) ([]model.Track, error) {
	reader, ok := m.sources[source]
	if !ok {
		return nil, errors.New("unsupported search platform")
	}
	tracks, err := reader.Search(ctx, keyword, limit, offset)
	if err == nil {
		return tracks, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !m.fallback.Load() || m.api == nil {
		return nil, fmt.Errorf("%s native search failed: %w", source, err)
	}
	m.logFallback(source, "search", err)
	tracks, fallbackErr := m.api.Search(ctx, source, keyword, limit, offset)
	if fallbackErr != nil {
		return nil, errors.Join(fmt.Errorf("native search: %w", err), fmt.Errorf("ChKSz search fallback: %w", fallbackErr))
	}
	return tracks, nil
}

func (m *Manager) Resolve(ctx context.Context, source string, track model.Track, quality string) (model.ResolvedTrack, error) {
	if m.api == nil {
		return model.ResolvedTrack{}, errors.New("audio resolver is not configured")
	}
	if source == "netease" || source == "qq" {
		return m.api.ResolvePlatform(ctx, source, track.ID, quality)
	}
	if source != "kugou" {
		return model.ResolvedTrack{}, errors.New("unsupported audio platform")
	}
	// Native Kugou hashes/audio IDs are not documented ChKSz IDs. Obtain its own
	// identifier and require a unique version match instead of guessing an ID.
	if strings.TrimSpace(track.Name) == "" || len(track.Artists) == 0 {
		return model.ResolvedTrack{}, errors.New("Kugou track lacks metadata required to confirm an audio-source ID")
	}
	tracks, err := m.api.Search(ctx, source, track.Name+" "+strings.Join(track.Artists, " "), 50, 0)
	if err != nil {
		return model.ResolvedTrack{}, err
	}
	id, err := MatchKugou(track, tracks)
	if err != nil {
		return model.ResolvedTrack{}, err
	}
	return m.api.ResolvePlatform(ctx, source, id, quality)
}

func (m *Manager) logFallback(source, operation string, cause error) {
	if m.logger != nil {
		m.logger.Warn("native metadata failed; using metered ChKSz fallback", "platform", source, "operation", operation, "reason", cause.Error())
	}
}

// MatchKugou rejects ambiguous versions instead of silently downloading a cover.
func MatchKugou(track model.Track, candidates []model.Track) (string, error) {
	exact := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.ID == "" {
			continue
		}
		if track.Hash != "" && (strings.EqualFold(track.Hash, candidate.Hash) || strings.EqualFold(track.Hash, candidate.ID)) {
			exact[candidate.ID] = true
		}
	}
	if len(exact) == 1 {
		for id := range exact {
			return id, nil
		}
	}
	if len(exact) > 1 {
		return "", errors.New("Kugou audio ID is ambiguous even after hash confirmation")
	}
	if normalize(track.Album) == "" || track.Duration <= 0 {
		return "", errors.New("Kugou audio ID cannot be confirmed without a matching hash or album and duration metadata")
	}
	matches := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.ID == "" || (track.Hash != "" && candidate.Hash != "" && !strings.EqualFold(track.Hash, candidate.Hash)) {
			continue
		}
		if normalize(track.Name) == "" || normalize(track.Name) != normalize(candidate.Name) || artistKey(track.Artists) == "" || artistKey(track.Artists) != artistKey(candidate.Artists) {
			continue
		}
		if track.Album != "" && normalize(track.Album) != normalize(candidate.Album) {
			continue
		}
		if track.Duration > 0 && (candidate.Duration <= 0 || abs(track.Duration-candidate.Duration) > 2) {
			continue
		}
		matches[candidate.ID] = true
	}
	if len(matches) == 1 {
		for id := range matches {
			return id, nil
		}
	}
	if len(matches) > 1 {
		return "", errors.New("Kugou audio source returned multiple matching versions; refusing to choose automatically")
	}
	return "", errors.New("Kugou audio source did not return a confirmed matching song")
}

func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

func artistKey(artists []string) string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		for _, part := range strings.FieldsFunc(artist, func(r rune) bool { return r == '、' || r == '/' || r == '&' || r == ';' || r == '；' }) {
			if name := normalize(part); name != "" {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
