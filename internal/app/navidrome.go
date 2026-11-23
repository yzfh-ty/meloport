package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"meloport/internal/config"
	"meloport/internal/model"
	"meloport/internal/navidrome"
	"meloport/internal/state"
)

// navidromePublisher references indexed audio without downloading or staging
// it. Local subscriptions and 115 subscriptions use their own path mappings.
type navidromePublisher struct {
	client *navidrome.Client
	config config.Cloud115
	store  *state.Store
}

func newNavidromePublisher(cfg config.Cloud115, store *state.Store) *navidromePublisher {
	client, _ := navidrome.New(navidrome.Options{
		BaseURL: cfg.NavidromeURL, Username: cfg.NavidromeUsername, Password: optionalEnv(cfg.NavidromePasswordEnv),
		MountRoot: cfg.NavidromeMountRoot, Timeout: cfg.Timeout.Duration,
	})
	return &navidromePublisher{client: client, config: cfg, store: store}
}

func (p *navidromePublisher) PublishPlaylist(ctx context.Context, task model.Task, playlist model.Playlist, downloads []state.Download) error {
	if !p.config.NavidromeEnabled || !p.config.NavidromePlaylistSync {
		return nil
	}
	if p.client == nil {
		return fmt.Errorf("Navidrome playlist sync credentials are unavailable or invalid; check %s", p.config.NavidromePasswordEnv)
	}
	if task.SourceType != "" && task.SourceType != "playlist" && task.SourceType != "ranking" {
		return errors.New("unsupported Navidrome playlist source")
	}
	provider := task.Platform
	if provider == "" {
		provider = "netease"
	}
	platformName := map[string]string{"netease": "网易云音乐", "qq": "QQ音乐", "kugou": "酷狗音乐"}[provider]
	if platformName == "" {
		return errors.New("unsupported Navidrome playlist platform")
	}
	byTrack := make(map[string]state.Download, len(downloads))
	for _, item := range downloads {
		if item.TaskID == task.ID && item.Provider == provider {
			byTrack[item.TrackID] = item
		}
	}
	candidates := make([]navidrome.Candidate, 0, len(playlist.Tracks))
	seen := make(map[string]bool, len(playlist.Tracks))
	for _, track := range playlist.Tracks {
		if seen[track.ID] {
			continue
		}
		seen[track.ID] = true
		item, found := byTrack[track.ID]
		if !found || !filepath.IsAbs(item.Path) {
			return fmt.Errorf("track %s has no saved file for Navidrome publication", track.ID)
		}
		relative, err := p.relativePath(ctx, task, item)
		if err != nil {
			return fmt.Errorf("track %s: %w", track.ID, err)
		}
		if track.Name == "" {
			track.Name = item.Title
		}
		if len(track.Artists) == 0 {
			track.Artists = item.Artists
		}
		if track.Album == "" {
			track.Album = item.Album
		}
		candidates = append(candidates, navidrome.Candidate{
			Title: track.Name, Artists: track.Artists, Album: track.Album, RelativePath: relative,
		})
	}
	name := fmt.Sprintf("%s - %s", platformName, playlist.Name)
	legacyName := fmt.Sprintf("%s [%s]", name, task.ID)
	if err := p.client.Publish(ctx, name, candidates, legacyName); err != nil {
		return fmt.Errorf("publish Navidrome playlist %q: %w", name, err)
	}
	return nil
}

func (p *navidromePublisher) relativePath(ctx context.Context, task model.Task, item state.Download) (string, error) {
	if p.config.Enabled {
		if p.store == nil {
			return "", errors.New("115 file mapping is unavailable for Navidrome publication")
		}
		file, found, err := p.store.GetCloudFile(ctx, p.config.TargetCID, task.ID, item.Provider, item.TrackID)
		if err != nil {
			return "", err
		}
		if !found || file.RelativePath == "" {
			return "", errors.New("audio has no saved 115 path for Navidrome publication")
		}
		return file.RelativePath, nil
	}
	if !filepath.IsAbs(task.MusicRoot) {
		return "", errors.New("local music root is unavailable for Navidrome publication")
	}
	relative, err := filepath.Rel(task.MusicRoot, item.Path)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("audio is outside the download root mapped to Navidrome")
	}
	return filepath.ToSlash(relative), nil
}
