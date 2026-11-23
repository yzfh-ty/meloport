package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var cloudSchema = []string{
	`CREATE TABLE cloud_files (
		root_cid TEXT NOT NULL,
		task_id TEXT NOT NULL,
		provider TEXT NOT NULL,
		track_id TEXT NOT NULL,
		local_path TEXT NOT NULL,
		staging_path TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		size INTEGER NOT NULL CHECK(size > 0),
		sha1 TEXT NOT NULL,
		media_id TEXT NOT NULL UNIQUE,
		published INTEGER NOT NULL DEFAULT 0,
		cid TEXT NOT NULL DEFAULT '',
		file_id TEXT NOT NULL DEFAULT '',
		pick_code TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(root_cid, task_id, provider, track_id),
		UNIQUE(root_cid, relative_path)
	)`,
	`PRAGMA user_version = 3`,
}

// CloudFile tracks a mounted 115 upload independently of the local download.
// A nonempty ID means the remote name, size and SHA-1 have all been verified.
// TargetPath uses the legacy staging_path SQLite column to preserve old mappings.
type CloudFile struct {
	RootCID, TaskID, Provider, TrackID  string
	LocalPath, TargetPath, RelativePath string
	Size                                int64
	SHA1, MediaID                       string
	Published                           bool
	CID, ID, PickCode                   string
}

const cloudColumns = `root_cid, task_id, provider, track_id, local_path, staging_path,
	relative_path, size, sha1, media_id, published, cid, file_id, pick_code`

func scanCloud(row rowScanner) (CloudFile, error) {
	var file CloudFile
	err := row.Scan(&file.RootCID, &file.TaskID, &file.Provider, &file.TrackID,
		&file.LocalPath, &file.TargetPath, &file.RelativePath, &file.Size, &file.SHA1,
		&file.MediaID, &file.Published, &file.CID, &file.ID, &file.PickCode)
	return file, err
}

func queryCloud(row rowScanner) (CloudFile, bool, error) {
	file, err := scanCloud(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CloudFile{}, false, nil
	}
	if err != nil {
		return CloudFile{}, false, fmt.Errorf("read 115 file mapping: %w", err)
	}
	return file, true, nil
}

func (s *Store) GetCloudFile(ctx context.Context, rootCID, taskID, provider, trackID string) (CloudFile, bool, error) {
	return queryCloud(s.db.QueryRowContext(ctx, `SELECT `+cloudColumns+` FROM cloud_files
		WHERE root_cid = ? AND task_id = ? AND provider = ? AND track_id = ?`, rootCID, taskID, provider, trackID))
}

func (s *Store) GetCloudByMediaID(ctx context.Context, id string) (CloudFile, bool, error) {
	return queryCloud(s.db.QueryRowContext(ctx, `SELECT `+cloudColumns+` FROM cloud_files
		WHERE media_id = ? AND file_id <> ''`, id))
}

func (s *Store) GetCloudByRemotePath(ctx context.Context, rootCID, relativePath string) (CloudFile, bool, error) {
	return queryCloud(s.db.QueryRowContext(ctx, `SELECT `+cloudColumns+` FROM cloud_files
		WHERE root_cid = ? AND relative_path = ? AND file_id <> ''`, rootCID, relativePath))
}

// SaveCloudIntent records the exact bytes before the file becomes visible to CloudDrive2.
// A retry can replace an unpublished intent while retaining its media ID.
func (s *Store) SaveCloudIntent(ctx context.Context, file CloudFile) (CloudFile, error) {
	if file.RootCID == "" || file.TaskID == "" || file.Provider == "" || file.TrackID == "" ||
		file.LocalPath == "" || file.TargetPath == "" || file.RelativePath == "" ||
		file.Size <= 0 || len(file.SHA1) != 40 || file.MediaID == "" {
		return CloudFile{}, errors.New("incomplete 115 publication record")
	}
	stored, err := scanCloud(s.db.QueryRowContext(ctx, `INSERT INTO cloud_files
		(root_cid, task_id, provider, track_id, local_path, staging_path, relative_path, size, sha1, media_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(root_cid, task_id, provider, track_id) DO UPDATE SET
		local_path = excluded.local_path, staging_path = excluded.staging_path,
		relative_path = excluded.relative_path, size = excluded.size, sha1 = excluded.sha1
		WHERE cloud_files.published = 0 AND cloud_files.file_id = ''
		RETURNING `+cloudColumns, file.RootCID, file.TaskID, file.Provider, file.TrackID,
		file.LocalPath, file.TargetPath, file.RelativePath, file.Size, file.SHA1, file.MediaID))
	if err != nil {
		return CloudFile{}, fmt.Errorf("record 115 publication intent: %w", err)
	}
	return stored, nil
}

func (s *Store) MarkCloudPublished(ctx context.Context, file CloudFile) error {
	result, err := s.db.ExecContext(ctx, `UPDATE cloud_files SET published = 1
		WHERE media_id = ? AND sha1 = ? AND size = ? AND relative_path = ? AND local_path = ? AND staging_path = ?`,
		file.MediaID, file.SHA1, file.Size, file.RelativePath, file.LocalPath, file.TargetPath)
	if err != nil {
		return fmt.Errorf("record CloudDrive2 publication handoff: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("115 publication record changed during handoff")
	}
	return nil
}

// ConfirmCloudFile only updates the exact intent that was verified remotely.
// A matching remote file can also recover a handoff interrupted before the
// published flag was saved.
func (s *Store) ConfirmCloudFile(ctx context.Context, file CloudFile) error {
	if file.CID == "" || file.ID == "" || file.PickCode == "" {
		return errors.New("incomplete confirmed 115 file mapping")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE cloud_files SET cid = ?, file_id = ?, pick_code = ?, published = 1
		WHERE media_id = ? AND sha1 = ? AND size = ? AND relative_path = ? AND local_path = ? AND staging_path = ? AND file_id = ''`,
		file.CID, file.ID, file.PickCode, file.MediaID, file.SHA1, file.Size, file.RelativePath, file.LocalPath, file.TargetPath)
	if err != nil {
		return fmt.Errorf("confirm 115 file mapping: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("115 publication record changed before confirmation")
	}
	return nil
}

func (s *Store) ListCloudFiles(ctx context.Context, rootCID string) ([]CloudFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cloudColumns+` FROM cloud_files
		WHERE root_cid = ? ORDER BY relative_path`, rootCID)
	if err != nil {
		return nil, fmt.Errorf("list 115 file mappings: %w", err)
	}
	defer rows.Close()
	var files []CloudFile
	for rows.Next() {
		file, err := scanCloud(rows)
		if err != nil {
			return nil, fmt.Errorf("read 115 file mapping: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate 115 file mappings: %w", err)
	}
	return files, nil
}
