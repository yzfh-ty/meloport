package state

import (
	"context"
	"fmt"
)

// AudioSearch filters database records before the caller inspects their files.
type AudioSearch struct {
	TaskID, Provider, Keyword string
	Offset, Limit             int
}

func (s *Store) SearchAudio(ctx context.Context, filter AudioSearch) ([]Download, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+downloadColumns+` FROM downloads
		WHERE (?1 = '' OR task_id = ?1) AND (?2 = '' OR provider = ?2)
		AND (?3 = '' OR instr(lower(title), lower(?3)) > 0 OR instr(lower(artists), lower(?3)) > 0
			OR instr(lower(album), lower(?3)) > 0 OR instr(lower(path), lower(?3)) > 0 OR instr(lower(track_id), lower(?3)) > 0)
		ORDER BY task_id, provider, track_id LIMIT ?4 OFFSET ?5`,
		filter.TaskID, filter.Provider, filter.Keyword, filter.Limit, filter.Offset)
	if err != nil {
		return nil, fmt.Errorf("search downloaded audio: %w", err)
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
		return nil, fmt.Errorf("iterate downloaded audio: %w", err)
	}
	return items, nil
}

// AudioTaskIDs includes tasks that were removed while their records were kept.
func (s *Store) AudioTaskIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT task_id FROM downloads ORDER BY task_id`)
	if err != nil {
		return nil, fmt.Errorf("list audio task IDs: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read audio task ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audio task IDs: %w", err)
	}
	return ids, nil
}
