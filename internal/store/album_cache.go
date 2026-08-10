package store

import (
	"context"
	"database/sql"
	"time"
)

// UpsertAlbumCache stores or refreshes the cached album.getData JSON for an album.
func (s *Store) UpsertAlbumCache(ctx context.Context, albID int64, data string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO album_cache (alb_id, data, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(alb_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		albID, data, time.Now().Unix())
	return err
}

// GetAlbumCache returns the cached album JSON, or ("", nil) if not cached.
func (s *Store) GetAlbumCache(ctx context.Context, albID int64) (string, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM album_cache WHERE alb_id = ?`, albID).Scan(&data)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return data, err
}
