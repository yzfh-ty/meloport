// Package state persists successful downloads and synchronization history in SQLite.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meloport/internal/model"

	_ "modernc.org/sqlite"
)

const schemaVersion = 4

type Store struct {
	db *sql.DB
}

type Download struct {
	TaskID         string      `json:"task_id"`
	Provider       string      `json:"provider"`
	Collection     string      `json:"-"` // Stable chart folder used for cloud handoff.
	TrackID        string      `json:"track_id"`
	Path           string      `json:"path"`
	Size           int64       `json:"size"`
	SHA256         string      `json:"sha256"`
	Title          string      `json:"title"`
	Artists        []string    `json:"artists"`
	Album          string      `json:"album"`
	Metadata       model.Track `json:"metadata"`
	TagFingerprint string      `json:"tag_fingerprint,omitempty"`
	TagPending     bool        `json:"tag_pending,omitempty"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type Run struct {
	ID            int64      `json:"id"`
	TaskID        string     `json:"task_id"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	PlaylistName  string     `json:"playlist_name"`
	Total         int        `json:"total"`
	ReportedCount int        `json:"reported_count"`
	Downloaded    int        `json:"downloaded"`
	Skipped       int        `json:"skipped"`
	Failed        int        `json:"failed"`
	Pending       int        `json:"pending"`
	Error         string     `json:"error,omitempty"`
}

func Open(path string) (*Store, error) { return open(path, false) }

// OpenReadOnly observes an existing database without creating or migrating it.
func OpenReadOnly(path string) (*Store, error) { return open(path, true) }

func open(path string, readOnly bool) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if !readOnly {
		if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Add("_pragma", "foreign_keys(ON)")
	}
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if !readOnly {
		if err := store.migrate(ctx); err != nil {
			db.Close()
			return nil, err
		}
		if err := os.Chmod(absolute, 0o600); err != nil {
			db.Close()
			return nil, fmt.Errorf("set database permissions: %w", err)
		}
	}
	return store, nil
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read database version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database version %d is newer than supported version %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin database migration: %w", err)
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS downloads (
			task_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			track_id TEXT NOT NULL,
			path TEXT NOT NULL,
			size INTEGER NOT NULL CHECK(size > 0),
			sha256 TEXT NOT NULL,
			title TEXT NOT NULL,
			artists TEXT NOT NULL,
			album TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(task_id, provider, track_id)
		)`,
		`CREATE TABLE IF NOT EXISTS runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			status TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			playlist_name TEXT NOT NULL DEFAULT '',
			total INTEGER NOT NULL DEFAULT 0,
			reported_count INTEGER NOT NULL DEFAULT 0,
			downloaded INTEGER NOT NULL DEFAULT 0,
			skipped INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			pending INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS runs_task_id_id ON runs(task_id, id DESC)`,
		`PRAGMA user_version = 1`,
	}
	if version == 0 {
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate database: %w", err)
			}
		}
	}
	if version < 2 {
		for _, statement := range []string{
			`ALTER TABLE downloads ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
			`ALTER TABLE downloads ADD COLUMN tag_fingerprint TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE downloads ADD COLUMN tag_pending INTEGER NOT NULL DEFAULT 0`,
			`CREATE INDEX IF NOT EXISTS downloads_path ON downloads(path)`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate audio tag state: %w", err)
			}
		}
	}
	for _, statement := range cloudSchema {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate 115 file state: %w", err)
		}
	}
	if version < 4 {
		var hasCollection bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('downloads') WHERE name = 'collection')`).Scan(&hasCollection); err != nil {
			return fmt.Errorf("inspect chart folder state: %w", err)
		}
		if !hasCollection {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE downloads ADD COLUMN collection TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate chart folder state: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
			return fmt.Errorf("record chart folder migration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit database migration: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// RecoverInterrupted must only be called by the process holding the writer lock.
func (s *Store) RecoverInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET status = 'interrupted', finished_at = ?,
		error = CASE WHEN error = '' THEN 'previous process ended before synchronization finished' ELSE error END
		WHERE status = 'running'`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("recover interrupted runs: %w", err)
	}
	return nil
}

const downloadColumns = `task_id, provider, track_id, path, size, sha256, title, artists, album, updated_at, metadata, tag_fingerprint, tag_pending, collection`

type rowScanner interface{ Scan(...any) error }

func scanDownload(row rowScanner) (Download, error) {
	var item Download
	var artists, metadata, updated string
	err := row.Scan(&item.TaskID, &item.Provider, &item.TrackID, &item.Path, &item.Size, &item.SHA256, &item.Title, &artists, &item.Album, &updated, &metadata, &item.TagFingerprint, &item.TagPending, &item.Collection)
	if err != nil {
		return Download{}, fmt.Errorf("read download record: %w", err)
	}
	if err := json.Unmarshal([]byte(artists), &item.Artists); err != nil {
		return Download{}, fmt.Errorf("decode stored artists: %w", err)
	}
	if err := json.Unmarshal([]byte(metadata), &item.Metadata); err != nil {
		return Download{}, fmt.Errorf("decode stored tag metadata: %w", err)
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Download{}, fmt.Errorf("decode download timestamp: %w", err)
	}
	return item, nil
}

func (s *Store) GetDownload(ctx context.Context, taskID, provider, trackID string) (Download, bool, error) {
	item, err := scanDownload(s.db.QueryRowContext(ctx, `SELECT `+downloadColumns+`
		FROM downloads WHERE task_id = ? AND provider = ? AND track_id = ?`, taskID, provider, trackID))
	if errors.Is(err, sql.ErrNoRows) {
		return Download{}, false, nil
	}
	if err != nil {
		return Download{}, false, err
	}
	return item, true, nil
}

func (s *Store) SaveDownload(ctx context.Context, item Download) error {
	return saveDownload(ctx, s.db, item)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func saveDownload(ctx context.Context, db execer, item Download) error {
	if item.TaskID == "" || item.Provider == "" || item.TrackID == "" || item.Path == "" || item.Size <= 0 {
		return errors.New("download record is incomplete")
	}
	item.Metadata.CoverURL = model.CanonicalCoverURL(item.Metadata.CoverURL)
	artists, err := json.Marshal(item.Artists)
	if err != nil {
		return fmt.Errorf("encode artists: %w", err)
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return fmt.Errorf("encode tag metadata: %w", err)
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `INSERT INTO downloads
		(task_id, provider, track_id, path, size, sha256, title, artists, album, updated_at, metadata, tag_fingerprint, tag_pending, collection)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id, provider, track_id) DO UPDATE SET
		path = excluded.path, size = excluded.size, sha256 = excluded.sha256, title = excluded.title,
		artists = excluded.artists, album = excluded.album, updated_at = excluded.updated_at,
		metadata = excluded.metadata, tag_fingerprint = excluded.tag_fingerprint, tag_pending = excluded.tag_pending,
		collection = excluded.collection`,
		item.TaskID, item.Provider, item.TrackID, item.Path, item.Size, item.SHA256, item.Title,
		string(artists), item.Album, item.UpdatedAt.UTC().Format(time.RFC3339Nano), string(metadata), item.TagFingerprint, item.TagPending, item.Collection)
	if err != nil {
		return fmt.Errorf("save download record: %w", err)
	}
	return nil
}

// CancelTag clears an update marker when the tagger left the original intact.
func (s *Store) CancelTag(ctx context.Context, item Download) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET tag_pending = 0
		WHERE path = ? AND size = ? AND sha256 = ?`, item.Path, item.Size, item.SHA256)
	if err != nil {
		return fmt.Errorf("clear pending audio tag update: %w", err)
	}
	return nil
}

// BeginTag durably marks a metadata update before its file changes.
// If the process or final database save fails, the next run retries tagging
// rather than treating the changed byte size as a damaged audio download.
func (s *Store) BeginTag(ctx context.Context, item Download) error {
	result, err := s.db.ExecContext(ctx, `UPDATE downloads SET tag_pending = 1
		WHERE path = ? AND size = ? AND sha256 = ? AND EXISTS (
			SELECT 1 FROM downloads WHERE task_id = ? AND provider = ? AND track_id = ? AND path = ? AND size = ? AND sha256 = ?
		)`, item.Path, item.Size, item.SHA256, item.TaskID, item.Provider, item.TrackID, item.Path, item.Size, item.SHA256)
	if err != nil {
		return fmt.Errorf("record pending audio tag update: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return errors.New("audio tag record no longer matches the file")
	}
	return nil
}

// FinishTag publishes the resulting file identity to all records that tracked
// the same original bytes at the same path, including records left by a task
// that was removed. Each of those records keeps its own song metadata.
func (s *Store) FinishTag(ctx context.Context, before, after Download) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audio tag state update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE downloads SET size = ?, sha256 = ?, tag_pending = 0,
		tag_fingerprint = CASE WHEN sha256 <> ? THEN '' ELSE tag_fingerprint END
		WHERE path = ? AND size = ? AND sha256 = ? AND tag_pending = 1`,
		after.Size, after.SHA256, after.SHA256, before.Path, before.Size, before.SHA256); err != nil {
		return fmt.Errorf("update audio tag file identity: %w", err)
	}
	if err := saveDownload(ctx, tx, after); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit audio tag state update: %w", err)
	}
	return nil
}

// ListDownloads returns durable local records, including songs no longer in a
// remote playlist. Closing rows before returning permits subsequent updates.
func (s *Store) ListDownloads(ctx context.Context, taskID, provider string) ([]Download, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+downloadColumns+` FROM downloads WHERE task_id = ? AND provider = ? ORDER BY track_id`, taskID, provider)
	if err != nil {
		return nil, fmt.Errorf("list downloaded tracks: %w", err)
	}
	defer rows.Close()
	var items []Download
	for rows.Next() {
		item, err := scanDownload(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate downloaded tracks: %w", err)
	}
	return items, nil
}

func (s *Store) StartRun(ctx context.Context, taskID string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO runs(task_id, status, started_at) VALUES (?, 'running', ?)`,
		taskID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("record run start: %w", err)
	}
	return result.LastInsertId()
}

func (s *Store) FinishRun(ctx context.Context, run Run) error {
	if run.ID <= 0 || run.Status == "" || run.Status == "running" {
		return errors.New("invalid finished run")
	}
	finished := time.Now().UTC()
	if run.FinishedAt != nil {
		finished = run.FinishedAt.UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ?, playlist_name = ?,
		total = ?, reported_count = ?, downloaded = ?, skipped = ?, failed = ?, pending = ?, error = ? WHERE id = ?`,
		run.Status, finished.Format(time.RFC3339Nano), run.PlaylistName, run.Total, run.ReportedCount,
		run.Downloaded, run.Skipped, run.Failed, run.Pending, run.Error, run.ID)
	if err != nil {
		return fmt.Errorf("record run completion: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check completed run: %w", err)
	}
	if count != 1 {
		return errors.New("run to finish does not exist")
	}
	return nil
}

func (s *Store) LatestRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, status, started_at, finished_at, playlist_name,
		total, reported_count, downloaded, skipped, failed, pending, error FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read run history: %w", err)
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		var run Run
		var started string
		var finished sql.NullString
		if err := rows.Scan(&run.ID, &run.TaskID, &run.Status, &started, &finished, &run.PlaylistName,
			&run.Total, &run.ReportedCount, &run.Downloaded, &run.Skipped, &run.Failed, &run.Pending, &run.Error); err != nil {
			return nil, fmt.Errorf("read run row: %w", err)
		}
		run.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, fmt.Errorf("decode run start: %w", err)
		}
		if finished.Valid {
			value, err := time.Parse(time.RFC3339Nano, finished.String)
			if err != nil {
				return nil, fmt.Errorf("decode run completion: %w", err)
			}
			run.FinishedAt = &value
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run history: %w", err)
	}
	return runs, nil
}
