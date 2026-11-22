package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"meloport/internal/cloud115"
	"meloport/internal/cloudsync"
	"meloport/internal/config"
	"meloport/internal/media"
	"meloport/internal/model"
	"meloport/internal/navidrome"
	"meloport/internal/state"
)

type cloudRuntime struct {
	sync            *cloudsync.Service
	handler         http.Handler
	drive           *cloudDrive
	login           *cloudLogin
	navidrome       *navidrome.Client
	navidromeConfig config.Cloud115
	navidromeReady  bool
}

func newCloudRuntime(cfg config.Cloud115, client *cloud115.Client, store *state.Store) *cloudRuntime {
	drive := &cloudDrive{}
	var service *cloudsync.Service
	var login *cloudLogin
	var handler http.Handler
	if cfg.Enabled {
		if client != nil {
			drive.environment = client
		}
		service = cloudsync.New(store, drive, cloudsync.Options{
			TargetCID: cfg.TargetCID, StagingDir: cfg.StagingDir, STRMDir: cfg.STRMDir, MediaBaseURL: cfg.MediaBaseURL,
		})
		login = &cloudLogin{
			auth:   cloud115.NewAuthClient(cfg.Timeout.Duration),
			client: func(token string) (cloudDriveClient, error) { return cloud115.New(token, cfg.Timeout.Duration) },
		}
	}
	if cfg.MediaEnabled {
		var lookup media.Lookup
		var resolver media.Resolver
		if cfg.Enabled {
			lookup, resolver = cloudLookup{store, cfg.TargetCID}, drive
		}
		var upstream, mountRoot string
		if cfg.NavidromeEnabled {
			upstream, mountRoot = cfg.NavidromeURL, cfg.NavidromeMountRoot
		}
		handler = media.New(lookup, resolver, upstream, mountRoot)
	}
	var navidromeClient *navidrome.Client
	var navidromeReady bool
	if cfg.Enabled && cfg.NavidromeEnabled && cfg.NavidromePlaylistSync {
		if password := optionalEnv(cfg.NavidromePasswordEnv); password != "" {
			navidromeClient, _ = navidrome.New(navidrome.Options{
				BaseURL: cfg.NavidromeURL, Username: cfg.NavidromeUsername, Password: password,
				MountRoot: cfg.NavidromeMountRoot, Timeout: cfg.Timeout.Duration,
			})
			navidromeReady = navidromeClient != nil
		}
	}
	return &cloudRuntime{
		sync: service, drive: drive,
		navidrome: navidromeClient, navidromeConfig: cfg, navidromeReady: navidromeReady,
		handler: handler, login: login,
	}
}

func (c *cloudRuntime) PublishPlaylist(ctx context.Context, task model.Task, playlist model.Playlist, downloads []state.Download) error {
	if !c.navidromeConfig.Enabled || !c.navidromeConfig.NavidromeEnabled || !c.navidromeConfig.NavidromePlaylistSync || task.SourceType != "ranking" {
		return nil
	}
	if c.navidrome == nil {
		return fmt.Errorf("Navidrome playlist sync credentials are unavailable or invalid; check %s", c.navidromeConfig.NavidromePasswordEnv)
	}
	provider := task.Platform
	if provider == "" {
		provider = "netease"
	}
	collection := model.CollectionFolder(task.ID, playlist.Name)
	byTrack := make(map[string]state.Download, len(downloads))
	for _, item := range downloads {
		if item.Provider == provider {
			byTrack[item.TrackID] = item
		}
	}
	candidates := make([]navidrome.Candidate, 0, len(playlist.Tracks))
	for _, track := range playlist.Tracks {
		item, found := byTrack[track.ID]
		if !found || !filepath.IsAbs(item.Path) || filepath.Base(item.Path) == "." {
			return fmt.Errorf("track %s has no saved file for Navidrome publication", track.ID)
		}
		candidates = append(candidates, navidrome.Candidate{
			Title: track.Name, Artists: track.Artists, Album: track.Album,
			RelativePath: filepath.ToSlash(filepath.Join(provider, collection, filepath.Base(item.Path))),
		})
	}
	platformName := map[string]string{"netease": "网易云音乐", "qq": "QQ音乐", "kugou": "酷狗音乐"}[provider]
	if platformName == "" {
		return errors.New("unsupported Navidrome playlist platform")
	}
	name := fmt.Sprintf("%s - %s [%s]", platformName, playlist.Name, task.ID)
	if err := c.navidrome.Publish(ctx, name, candidates); err != nil {
		return fmt.Errorf("publish Navidrome playlist %q: %w", name, err)
	}
	return nil
}

var errCloudLoginRequired = errors.New("sign in to 115 in Meloport or set the configured access token environment variable")

type cloudDriveClient interface {
	UserInfo(context.Context) (cloud115.User, error)
	List(context.Context, string) ([]cloud115.File, error)
	DownURL(context.Context, string, string) (string, error)
}

type cloudAccountView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cloudDrive struct {
	mu          sync.Mutex
	environment cloudDriveClient
	session     cloudDriveClient
	expires     time.Time
	account     *cloudAccountView
}

func (d *cloudDrive) current() (cloudDriveClient, bool, *cloudAccountView) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session != nil && time.Now().Before(d.expires) {
		return d.session, true, d.account
	}
	d.session, d.account = nil, nil
	return d.environment, false, nil
}

func (d *cloudDrive) setSession(client cloudDriveClient, expires time.Time, account *cloudAccountView) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.session, d.expires, d.account = client, expires, account
}

func (d *cloudDrive) List(ctx context.Context, cid string) ([]cloud115.File, error) {
	client, _, _ := d.current()
	if client == nil {
		return nil, errCloudLoginRequired
	}
	return client.List(ctx, cid)
}

func (d *cloudDrive) DownURL(ctx context.Context, pickCode, userAgent string) (string, error) {
	client, _, _ := d.current()
	if client == nil {
		return "", errCloudLoginRequired
	}
	return client.DownURL(ctx, pickCode, userAgent)
}

func (c *cloudRuntime) Ready(ctx context.Context, taskID, provider, trackID string) (bool, error) {
	ready, err := c.sync.Ready(ctx, taskID, provider, trackID)
	if err != nil || ready {
		return ready, err
	}
	if client, _, _ := c.drive.current(); client == nil {
		return false, errCloudLoginRequired
	}
	return false, nil
}

func (c *cloudRuntime) Stage(ctx context.Context, item state.Download) (bool, error) {
	if client, _, _ := c.drive.current(); client == nil {
		return false, errCloudLoginRequired
	}
	return c.sync.Stage(ctx, item)
}

type cloudLookup struct {
	store   *state.Store
	rootCID string
}

func (l cloudLookup) ByID(ctx context.Context, id string) (string, bool, error) {
	item, found, err := l.store.GetCloudByMediaID(ctx, id)
	if err != nil || !found || item.RootCID != l.rootCID || item.PickCode == "" {
		return "", false, err
	}
	return item.PickCode, true, nil
}

func (l cloudLookup) ByPath(ctx context.Context, path string) (string, bool, error) {
	item, found, err := l.store.GetCloudByRemotePath(ctx, l.rootCID, path)
	if err != nil || !found || item.PickCode == "" {
		return "", false, err
	}
	return item.PickCode, true, nil
}

func cloudClient(cfg config.Cloud115) (*cloud115.Client, error) {
	if !cfg.Enabled {
		return nil, errors.New("enable cloud115 in the configuration first")
	}
	token := optionalEnv(cfg.AccessTokenEnv)
	if token == "" {
		return nil, fmt.Errorf("set the 115 access token in %s", cfg.AccessTokenEnv)
	}
	return cloud115.New(token, cfg.Timeout.Duration)
}

func checkCloud(ctx context.Context, client *cloud115.Client, cid string, downURL bool, output io.Writer) error {
	if _, err := client.UserInfo(ctx); err != nil {
		return fmt.Errorf("verify 115 authorization: %w", err)
	}
	files, err := client.List(ctx, cid)
	if err != nil {
		return fmt.Errorf("list 115 target: %w", err)
	}
	if _, err := fmt.Fprintf(output, "115 authorization verified. Target directory contains %d entries.\n", len(files)); err != nil || !downURL {
		return err
	}
	checkFile := func(files []cloud115.File) (bool, error) {
		for _, file := range files {
			if !file.IsDir && !file.Incomplete && file.PickCode != "" {
				_, err := client.DownURL(ctx, file.PickCode, "Meloport/115-check")
				return true, err
			}
		}
		return false, nil
	}
	found, err := checkFile(files)
	if !found {
		// New uploads use platform/chart directories. Keep this bounded so a
		// check against an empty account never walks the whole drive.
		scanned := 0
		var scan func([]cloud115.File, int) (bool, error)
		scan = func(entries []cloud115.File, depth int) (bool, error) {
			if found, err := checkFile(entries); found || err != nil || depth == 0 {
				return found, err
			}
			for _, dir := range entries {
				if !dir.IsDir || scanned >= 30 {
					continue
				}
				scanned++
				children, listErr := client.List(ctx, dir.ID)
				if listErr != nil {
					return false, fmt.Errorf("list 115 collection directory: %w", listErr)
				}
				if found, err := scan(children, depth-1); found || err != nil {
					return found, err
				}
			}
			return false, nil
		}
		found, err = scan(files, 3)
	}
	if err != nil {
		return fmt.Errorf("resolve 115 download URL: %w", err)
	}
	if !found {
		return errors.New("no completed file in the target or the first ten task directories to check downurl")
	}
	_, err = fmt.Fprintln(output, "115 download URL resolved successfully.")
	return err
}

func (c *cloudRuntime) poll(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	for ctx.Err() == nil {
		if err := c.sync.Reconcile(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, errCloudLoginRequired) {
			logger.Warn("115 reconciliation failed", "error", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
