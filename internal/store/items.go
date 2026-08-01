package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Item is a queue row: one Deezer track and its download/import state.
type Item struct {
	SngID       int64
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	GroupKey    string
	SourceType  string
	SourceID    string
	State       string
	Stage       string
	Format      string
	FilePath    string
	TmpPath     string
	BytesDone   int64
	Attempts    int
	Error       string
	CreatedAt   int64
	UpdatedAt   int64
}

// Discovered is the metadata a sync knows about a track before downloading.
type Discovered struct {
	SngID       int64
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	GroupKey    string
	SourceType  string
	SourceID    string
}

const itemColumns = `sng_id, title, artist, album, album_artist, group_key,
	source_type, source_id, state, stage, format, file_path, tmp_path,
	bytes_done, attempts, error, created_at, updated_at`

func scanItem(row interface{ Scan(...any) error }) (*Item, error) {
	var it Item
	err := row.Scan(&it.SngID, &it.Title, &it.Artist, &it.Album, &it.AlbumArtist,
		&it.GroupKey, &it.SourceType, &it.SourceID, &it.State, &it.Stage,
		&it.Format, &it.FilePath, &it.TmpPath, &it.BytesDone, &it.Attempts,
		&it.Error, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// Upsert records a discovered track. New tracks are inserted as `waiting`
// (or `blocklisted` if their track/album/artist is blocked). Existing tracks
// keep their state and download progress — only metadata is refreshed — so a
// re-sync is idempotent and never resurrects finished work. Returns true if a
// new row was inserted.
func (s *Store) Upsert(ctx context.Context, d Discovered) (bool, error) {
	now := time.Now().Unix()

	initial := StateWaiting
	blocked, err := s.IsBlocked(ctx, "track", d.SngID)
	if err != nil {
		return false, err
	}
	if blocked {
		initial = StateBlocklisted
	}

	// Insert only if new; the initial state (waiting/blocklisted) is set here and
	// never overwritten afterwards.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO items (`+itemColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', '', 0, 0, '', ?, ?)
		ON CONFLICT(sng_id) DO NOTHING`,
		d.SngID, d.Title, d.Artist, d.Album, d.AlbumArtist, d.GroupKey,
		d.SourceType, d.SourceID, initial, now, now)
	if err != nil {
		return false, err
	}
	if affected, _ := res.RowsAffected(); affected == 1 {
		return true, nil
	}

	// Existing row: refresh metadata only, preserving state and progress so the
	// re-sync is idempotent.
	_, err = s.db.ExecContext(ctx, `
		UPDATE items SET title = ?, artist = ?, album = ?, album_artist = ?,
			group_key = ?, source_type = ?, source_id = ?, updated_at = ?
		WHERE sng_id = ?`,
		d.Title, d.Artist, d.Album, d.AlbumArtist, d.GroupKey,
		d.SourceType, d.SourceID, now, d.SngID)
	return false, err
}

// Get returns a single item, or (nil, nil) if absent.
func (s *Store) Get(ctx context.Context, sngID int64) (*Item, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM items WHERE sng_id = ?`, sngID)
	it, err := scanItem(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return it, err
}

// List returns items in the given states (all states if empty), newest first.
func (s *Store) List(ctx context.Context, states []string, limit int) ([]*Item, error) {
	q := `SELECT ` + itemColumns + ` FROM items`
	args := []any{}
	if len(states) > 0 {
		placeholders := make([]string, len(states))
		for i, st := range states {
			placeholders[i] = "?"
			args = append(args, st)
		}
		q += ` WHERE state IN (` + strings.Join(placeholders, ",") + `)`
	}
	q += ` ORDER BY updated_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountByState returns the number of items per state.
func (s *Store) CountByState(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM items GROUP BY state`)
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

// ClaimDownload atomically claims the oldest pending track for downloading,
// incrementing its attempt counter. Returns (nil, false, nil) when the queue is
// empty. Progress fields (tmp_path/bytes_done) are preserved so a claimed item
// resumes rather than restarts.
func (s *Store) ClaimDownload(ctx context.Context) (*Item, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE items SET state = ?, stage = '', attempts = attempts + 1, updated_at = ?
		WHERE sng_id = (
			SELECT sng_id FROM items
			WHERE state IN (?, ?)
			ORDER BY updated_at ASC LIMIT 1
		)
		RETURNING `+itemColumns,
		StateDownloading, time.Now().Unix(), StateWaiting, StateQueued)
	it, err := scanItem(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return it, true, nil
}

// ClaimImport atomically claims the oldest downloaded track for importing.
func (s *Store) ClaimImport(ctx context.Context) (*Item, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE items SET state = ?, updated_at = ?
		WHERE sng_id = (
			SELECT sng_id FROM items
			WHERE state IN (?, ?)
			ORDER BY updated_at ASC LIMIT 1
		)
		RETURNING `+itemColumns,
		StateImporting, time.Now().Unix(), StateDownloaded, StateImportQueued)
	it, err := scanItem(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return it, true, nil
}

// UpdateProgress records resumable download progress.
func (s *Store) UpdateProgress(ctx context.Context, sngID, bytesDone int64, tmpPath string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET bytes_done = ?, tmp_path = ?, updated_at = ? WHERE sng_id = ?`,
		bytesDone, tmpPath, time.Now().Unix(), sngID)
	return err
}

// MarkDownloaded moves an item to `downloaded` (ready for import), recording the
// resolved format and final file path and clearing transient progress.
func (s *Store) MarkDownloaded(ctx context.Context, sngID int64, format, filePath string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE items SET state = ?, stage = '', format = ?, file_path = ?,
			tmp_path = '', error = '', updated_at = ? WHERE sng_id = ?`,
		StateDownloaded, format, filePath, time.Now().Unix(), sngID)
	return err
}

// MarkFinished moves an item to its terminal success state.
func (s *Store) MarkFinished(ctx context.Context, sngID int64) error {
	return s.SetState(ctx, sngID, StateFinished)
}

// MarkFailed records a failure at the given stage.
func (s *Store) MarkFailed(ctx context.Context, sngID int64, stage, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, stage = ?, error = ?, updated_at = ? WHERE sng_id = ?`,
		StateFailed, stage, errMsg, time.Now().Unix(), sngID)
	return err
}

// SetState sets an item's state, clearing the error field.
func (s *Store) SetState(ctx context.Context, sngID int64, state string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = '', updated_at = ? WHERE sng_id = ?`,
		state, time.Now().Unix(), sngID)
	return err
}

// RecoverInterrupted resets in-flight rows after a daemon restart: downloading
// tracks go back to queued (keeping tmp_path/bytes_done to resume) and importing
// tracks go back to downloaded. Returns the number of rows recovered.
func (s *Store) RecoverInterrupted(ctx context.Context) (int, error) {
	now := time.Now().Unix()
	r1, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, updated_at = ? WHERE state = ?`,
		StateQueued, now, StateDownloading)
	if err != nil {
		return 0, err
	}
	r2, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, updated_at = ? WHERE state = ?`,
		StateDownloaded, now, StateImporting)
	if err != nil {
		return 0, err
	}
	a, _ := r1.RowsAffected()
	b, _ := r2.RowsAffected()
	return int(a + b), nil
}

// Requeue moves the given tracks back to `queued` for (re)downloading. When
// clearFile is true the recorded file_path and progress are cleared too — used
// by force-all re-downloads. Returns the number of rows changed.
func (s *Store) Requeue(ctx context.Context, sngIDs []int64, clearFile bool) (int, error) {
	if len(sngIDs) == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	set := `state = ?, stage = '', error = '', bytes_done = 0, tmp_path = ''`
	if clearFile {
		set += `, file_path = ''`
	}
	// Placeholder order: state (SET), now (updated_at), then the IN ids.
	placeholders := make([]string, len(sngIDs))
	args := []any{StateQueued, now}
	for i, id := range sngIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`UPDATE items SET %s, updated_at = ? WHERE sng_id IN (%s)`,
		set, strings.Join(placeholders, ","))
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// FinishedItems returns all items whose state is finished or downloaded (i.e.
// they have a file_path on disk). Used by `verify` and force-missing.
func (s *Store) FinishedItems(ctx context.Context) ([]*Item, error) {
	return s.List(ctx, []string{StateFinished, StateDownloaded}, 0)
}
