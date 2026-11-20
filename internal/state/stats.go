package state

import "context"

type Statistics struct {
	Downloaded int64 `json:"downloaded"`
	Bytes      int64 `json:"bytes"`
}

// Statistics reports registered files; it does not rescan the music directory.
func (s *Store) Statistics(ctx context.Context) (Statistics, error) {
	var stats Statistics
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM downloads`).Scan(&stats.Downloaded, &stats.Bytes)
	return stats, err
}
