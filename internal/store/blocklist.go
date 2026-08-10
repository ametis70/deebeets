package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Block is a blocklist entry.
type Block struct {
	Kind      string
	ExtID     int64
	Reason    string
	CreatedAt int64
}

// Valid blocklist kinds.
const (
	KindTrack    = "track"
	KindAlbum    = "album"
	KindArtist   = "artist"
	KindPlaylist = "playlist"
)

// AddBlock adds (or updates the reason of) a blocklist entry. Blocking a track
// also flips any existing item for that track to `blocklisted` so it stops
// being downloaded; album/artist/playlist blocks take effect at the next sync.
func (s *Store) AddBlock(ctx context.Context, kind string, extID int64, reason string) error {
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO blocklist(kind, ext_id, reason, created_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(kind, ext_id) DO UPDATE SET reason = excluded.reason`,
		kind, extID, reason, now); err != nil {
		return err
	}
	if kind == KindTrack {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE items SET state = ?, updated_at = ? WHERE sng_id = ? AND state != ?`,
			StateBlocklisted, now, extID, StateConverted); err != nil {
			return err
		}
	}
	return nil
}

// RemoveBlock deletes a blocklist entry. Tracks previously blocked are reset to
// `waiting` so they can be picked up again.
func (s *Store) RemoveBlock(ctx context.Context, kind string, extID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM blocklist WHERE kind = ? AND ext_id = ?`, kind, extID); err != nil {
		return err
	}
	if kind == KindTrack {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE items SET state = ?, updated_at = ? WHERE sng_id = ? AND state = ?`,
			StateWaiting, time.Now().Unix(), extID, StateBlocklisted); err != nil {
			return err
		}
	}
	return nil
}

// IsBlocked reports whether (kind, extID) is blocklisted.
func (s *Store) IsBlocked(ctx context.Context, kind string, extID int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM blocklist WHERE kind = ? AND ext_id = ?`, kind, extID).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListBlocks returns all blocklist entries, newest first.
func (s *Store) ListBlocks(ctx context.Context) ([]Block, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, ext_id, reason, created_at FROM blocklist ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Block
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.Kind, &b.ExtID, &b.Reason, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
