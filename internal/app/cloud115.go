package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"meloport/internal/cloud115"
	"meloport/internal/cloudsync"
	"meloport/internal/config"
	"meloport/internal/media"
	"meloport/internal/state"
)

type cloudRuntime struct {
	sync    *cloudsync.Service
	handler http.Handler
}

func newCloudRuntime(cfg config.Cloud115, client *cloud115.Client, store *state.Store) *cloudRuntime {
	service := cloudsync.New(store, client, cloudsync.Options{
		TargetCID: cfg.TargetCID, StagingDir: cfg.StagingDir, STRMDir: cfg.STRMDir, MediaBaseURL: cfg.MediaBaseURL,
	})
	runtime := &cloudRuntime{sync: service}
	if client != nil {
		runtime.handler = media.New(cloudLookup{store, cfg.TargetCID}, client, cfg.NavidromeURL, cfg.NavidromeMountRoot)
	}
	return runtime
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
		// CD2 normally preserves one subdirectory per task. Bound the check
		// so pointing it at an empty library does not scan the entire account.
		scanned := 0
		for _, dir := range files {
			if !dir.IsDir || scanned == 10 {
				continue
			}
			scanned++
			children, listErr := client.List(ctx, dir.ID)
			if listErr != nil {
				return fmt.Errorf("list 115 task directory: %w", listErr)
			}
			if found, err = checkFile(children); found {
				break
			}
		}
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
		if err := c.sync.Reconcile(ctx); err != nil && ctx.Err() == nil {
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
