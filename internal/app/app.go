// Package app wires the command line, scheduler, provider and durable state.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"meloport/internal/chksz"
	"meloport/internal/config"
	"meloport/internal/download"
	"meloport/internal/model"
	"meloport/internal/platform"
	"meloport/internal/platform/kugou"
	"meloport/internal/platform/netease"
	"meloport/internal/platform/qq"
	"meloport/internal/provider"
	"meloport/internal/state"
	"meloport/internal/syncer"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) error {
	command := "serve"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}
	switch command {
	case "version":
		if len(args) != 0 {
			return errors.New("version takes no arguments")
		}
		_, err := fmt.Fprintln(stdout, version)
		return err
	case "serve", "sync", "retag", "validate", "status", "playlist", "search":
	default:
		return errors.New("unknown command; use serve, sync, retag, playlist, search, validate, status or version")
	}
	flags := flag.NewFlagSet("meloport "+command, flag.ContinueOnError)
	// flag's default diagnostics echo invalid values, which may contain a key.
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config.yaml", "configuration YAML path")
	var taskID *string
	var webDir *string
	var limit *int
	var searchPlatform, keyword *string
	var offset *int
	if command == "sync" || command == "playlist" {
		taskID = flags.String("task", "", "run one enabled task by ID; empty runs all enabled tasks")
	}
	if command == "retag" {
		taskID = flags.String("task", "", "fill missing tags for one task by ID; empty processes all configured tasks")
	}
	if command == "search" {
		searchPlatform = flags.String("platform", "netease", "netease, qq or kugou")
		keyword = flags.String("keyword", "", "song title, artist or album query")
		limit = flags.Int("limit", 20, "number of results, 1-50")
		offset = flags.Int("offset", 0, "result offset")
	}
	if command == "serve" {
		webDir = flags.String("web-dir", "", "serve live Web assets from this directory (development only)")
	}
	if command == "status" {
		limit = flags.Int("limit", 20, "number of recent task runs, 1-1000")
	}
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Meloport: scheduled playlist downloads. Commands: serve, sync, retag, playlist, search, validate, status, version.")
		fmt.Fprintf(stderr, "Usage: meloport %s [options]\n", command)
		flags.SetOutput(stderr)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid command options; use -help")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; use -help")
	}
	if command == "serve" {
		if err := config.Ensure(*path); err != nil {
			return err
		}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if command == "validate" {
		fmt.Fprintf(stdout, "Configuration valid. Timezone: %s; tasks: %d.\n", cfg.Timezone, len(cfg.Tasks))
		for _, task := range cfg.Tasks {
			fmt.Fprintf(stdout, "  %s enabled=%t schedule=%q quality=%s\n", task.ID, task.IsEnabled(), task.Schedule, task.Quality)
		}
		return nil
	}
	if command == "status" {
		if *limit < 1 || *limit > 1000 {
			return errors.New("limit must be between 1 and 1000")
		}
		store, err := state.OpenReadOnly(cfg.Storage.Database)
		if err != nil {
			return fmt.Errorf("open state (run a sync first): %w", err)
		}
		defer store.Close()
		runs, err := store.LatestRuns(ctx, *limit)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(runs)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	metadataHTTP := platform.NewHTTP(nil, cfg.Metadata.Timeout.Duration, cfg.Metadata.MinInterval.Duration)
	gateway := chksz.NewLazy(chksz.Options{BaseURL: cfg.API.BaseURL, Timeout: cfg.API.Timeout.Duration, MinInterval: cfg.API.MinInterval.Duration}, cfg.API.KeyEnv)
	metadata := provider.New(map[string]provider.Source{
		"netease": netease.New(netease.Options{HTTP: metadataHTTP, Cookie: optionalEnv(cfg.Metadata.NeteaseCookieEnv)}),
		"qq":      qq.New(qq.Options{HTTP: metadataHTTP, Cookie: optionalEnv(cfg.Metadata.QQCookieEnv)}),
		"kugou":   kugou.New(kugou.Options{HTTP: metadataHTTP, Cookie: optionalEnv(cfg.Metadata.KugouCookieEnv)}),
	}, gateway, cfg.Metadata.FallbackToAPI, logger)
	if command == "search" {
		if *limit < 1 || *limit > 50 || *offset < 0 || *offset > 100000 || *keyword == "" || len([]rune(*keyword)) > 300 {
			return errors.New("search requires -keyword, -limit 1-50 and -offset 0-100000")
		}
		tracks, err := metadata.Search(ctx, *searchPlatform, *keyword, *limit, *offset)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(struct {
			Platform string        `json:"platform"`
			Tracks   []model.Track `json:"tracks"`
		}{*searchPlatform, tracks})
	}
	if command == "playlist" {
		var found *config.Task
		for i := range cfg.Tasks {
			if (taskID != nil && *taskID != "" && cfg.Tasks[i].ID == *taskID) || ((taskID == nil || *taskID == "") && len(cfg.Tasks) == 1) {
				found = &cfg.Tasks[i]
				break
			}
		}
		if found == nil {
			return errors.New("playlist preview requires -task matching a configured task")
		}
		task := found.Model()
		playlist, err := metadata.PlaylistFor(ctx, task.Platform, task.SourceType, task.SourceID)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(playlist)
	}
	var selected []config.Task
	for _, task := range cfg.Tasks {
		if (command == "retag" || task.IsEnabled()) && (taskID == nil || *taskID == "" || *taskID == task.ID) {
			selected = append(selected, task)
		}
	}
	if len(selected) == 0 && command == "sync" {
		return errors.New("no enabled tasks match; check tasks and -task")
	}
	if len(selected) == 0 && command == "retag" {
		return errors.New("no configured tasks match; check tasks and -task")
	}
	downloader, err := download.New(download.Options{Timeout: cfg.Download.Timeout.Duration, MaxBytes: cfg.Download.MaxSizeMB * 1024 * 1024})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Storage.Database), 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	lock := flock.New(cfg.Storage.Database + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer lock.Close()
	if !locked {
		return errors.New("another Meloport process is using this database; stop it before starting another writer")
	}
	store, err := state.Open(cfg.Storage.Database)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.RecoverInterrupted(ctx); err != nil {
		return err
	}
	engine := syncer.NewWithSources(metadata, metadata, downloader, store)
	if command == "retag" {
		enc := json.NewEncoder(stdout)
		var runErrors []error
		for _, task := range selected {
			summary, err := engine.Retag(ctx, task.Model())
			if encodeErr := enc.Encode(summary); encodeErr != nil {
				return encodeErr
			}
			if err != nil {
				runErrors = append(runErrors, fmt.Errorf("retag %s: %w", task.ID, err))
				if ctx.Err() != nil {
					break
				}
			}
		}
		return errors.Join(runErrors...)
	}
	if command == "sync" {
		enc := json.NewEncoder(stdout)
		var runErrors []error
		for _, task := range selected {
			summary, runErr := engine.Run(ctx, task.Model())
			if err := enc.Encode(summary); err != nil {
				return err
			}
			if runErr != nil {
				runErrors = append(runErrors, runErr)
				if ctx.Err() != nil || chksz.IsAccountError(runErr) {
					break
				}
			}
		}
		return errors.Join(runErrors...)
	}
	assetDirectory := ""
	if webDir != nil && *webDir != "" {
		assetDirectory = *webDir
		if !filepath.IsAbs(assetDirectory) {
			assetDirectory = filepath.Join(filepath.Dir(*path), assetDirectory)
		}
		assetDirectory, err = filepath.Abs(assetDirectory)
		if err != nil {
			return errors.New("invalid Web asset directory")
		}
	}
	return serve(ctx, cfg, selected, engine, store, logger, serviceOptions{ConfigPath: *path, WebDir: assetDirectory, Store: store, Metadata: metadata})
}

func optionalEnv(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

// cleanupContext allows run persistence even after the process received a stop signal.
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
