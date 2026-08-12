package store

import (
	"context"
	"database/sql"
	"time"
)

// Source kinds.
const (
	SourceKindAlbum    = "album"
	SourceKindArtist   = "artist"
	SourceKindPlaylist = "playlist"
)

// Source states.
const (
	SourceStateWaiting = "waiting"
	SourceStateSyncing = "syncing"
	SourceStateSynced  = "synced"
	SourceStateFailed  = "failed"
)

// Source is a top-level favorite: an album, artist, or playlist.
type Source struct {
	Kind          string
	ExtID         int64
	Name          string
	Artist        string // album artist (empty for artists/playlists)
	State         string
	TrackCount    int
	Error         string
	DeezerStatus  string // PRESENT | REPLACED | MISSING
	ReplacementID int64  // ALB_ID of the replacement (0 if none)
	CreatedAt     int64
	UpdatedAt     int64
}

const sourceColumns = `kind, ext_id, name, artist, state, track_count, error,
	deezer_status, replacement_id, created_at, updated_at`

func scanSource(row interface{ Scan(...any) error }) (*Source, error) {
	var s Source
	err := row.Scan(&s.Kind, &s.ExtID, &s.Name, &s.Artist,
		&s.State, &s.TrackCount, &s.Error,
		&s.DeezerStatus, &s.ReplacementID,
		&s.CreatedAt, &s.UpdatedAt)
	return &s, err
}

// UpsertSource inserts or updates a source row. Existing rows preserve their
// state — only name and artist metadata are refreshed. Returns true if inserted.
func (s *Store) UpsertSource(ctx context.Context, kind string, extID int64, name, artist string) (bool, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO sources (`+sourceColumns+`)
		VALUES (?, ?, ?, ?, ?, 0, '', ?, 0, ?, ?)
		ON CONFLICT(kind, ext_id) DO UPDATE SET
			name       = excluded.name,
			artist     = excluded.artist,
			updated_at = excluded.updated_at`,
		kind, extID, name, artist, SourceStateWaiting, DeezerStatusPresent, now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetSourceDeezerStatus updates the deezer_status and optional replacement_id of a source.
func (s *Store) SetSourceDeezerStatus(ctx context.Context, kind string, extID int64, status string, replacementID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sources SET deezer_status = ?, replacement_id = ?, updated_at = ?
		WHERE kind = ? AND ext_id = ?`,
		status, replacementID, time.Now().Unix(), kind, extID)
	return err
}

// CountSourcesByDeezerStatus returns counts of sources per deezer_status for the given kind.
func (s *Store) CountSourcesByDeezerStatus(ctx context.Context, kind string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT deezer_status, COUNT(*) FROM sources WHERE kind = ? GROUP BY deezer_status`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// SetSourceState updates the state (and optionally error) of a source.
func (s *Store) SetSourceState(ctx context.Context, kind string, extID int64, state, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sources SET state = ?, error = ?, updated_at = ?
		WHERE kind = ? AND ext_id = ?`,
		state, errMsg, time.Now().Unix(), kind, extID)
	return err
}

// SetSourceTrackCount records how many tracks were found for a source.
func (s *Store) SetSourceTrackCount(ctx context.Context, kind string, extID int64, count int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sources SET track_count = ?, updated_at = ? WHERE kind = ? AND ext_id = ?`,
		count, time.Now().Unix(), kind, extID)
	return err
}

// GetSource returns a single source, or (nil, nil) if absent.
func (s *Store) GetSource(ctx context.Context, kind string, extID int64) (*Source, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+sourceColumns+` FROM sources WHERE kind = ? AND ext_id = ?`, kind, extID)
	src, err := scanSource(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return src, err
}

// ListSources returns sources of the given kinds (all if empty), newest first.
func (s *Store) ListSources(ctx context.Context, kinds []string) ([]*Source, error) {
	q := `SELECT ` + sourceColumns + ` FROM sources`
	var args []any
	if len(kinds) > 0 {
		q += ` WHERE kind IN (`
		for i, k := range kinds {
			if i > 0 {
				q += ","
			}
			q += "?"
			args = append(args, k)
		}
		q += `)`
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListSourcesByState returns sources in the given state.
func (s *Store) ListSourcesByState(ctx context.Context, state string) ([]*Source, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sourceColumns+` FROM sources WHERE state = ? ORDER BY updated_at ASC`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}
