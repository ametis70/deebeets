package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Item is a queue row: one Deezer track and its download state.
type Item struct {
	SngID          int64
	Title          string
	Artist         string
	Album          string
	AlbumArtist    string
	GroupKey       string
	SourceType     string
	SourceID       string
	State          string
	Stage          string
	Format         string
	FilePath       string
	Attempts       int
	BatchAttempts  int
	InFailedBatch  bool
	Error          string
	CreatedAt      int64
	UpdatedAt      int64
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
	source_type, source_id, state, stage, format, file_path,
	attempts, batch_attempts, in_failed_batch, error, created_at, updated_at`

func scanItem(row interface{ Scan(...any) error }) (*Item, error) {
	var it Item
	var inFailed int
	err := row.Scan(&it.SngID, &it.Title, &it.Artist, &it.Album, &it.AlbumArtist,
		&it.GroupKey, &it.SourceType, &it.SourceID, &it.State, &it.Stage,
		&it.Format, &it.FilePath,
		&it.Attempts, &it.BatchAttempts, &inFailed,
		&it.Error, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		return nil, err
	}
	it.InFailedBatch = inFailed == 1
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

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO items (`+itemColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', 0, 0, 0, '', ?, ?)
		ON CONFLICT(sng_id) DO NOTHING`,
		d.SngID, d.Title, d.Artist, d.Album, d.AlbumArtist, d.GroupKey,
		d.SourceType, d.SourceID, initial, now, now)
	if err != nil {
		return false, err
	}
	if affected, _ := res.RowsAffected(); affected == 1 {
		return true, nil
	}

	// Existing row: refresh metadata only, preserving state and progress.
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
// incrementing its attempt counter. Returns (nil, false, nil) when the queue
// is empty.
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

// MarkDownloaded moves an item to `downloaded`, recording the resolved format
// and final file path.
func (s *Store) MarkDownloaded(ctx context.Context, sngID int64, format, filePath string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE items SET state = ?, stage = '', format = ?, file_path = ?,
			error = '', updated_at = ? WHERE sng_id = ?`,
		StateDownloaded, format, filePath, time.Now().Unix(), sngID)
	return err
}

// MarkFinished moves an item to its terminal success state.
func (s *Store) MarkFinished(ctx context.Context, sngID int64) error {
	return s.SetState(ctx, sngID, StateFinished)
}

// MarkAllDownloadedFinished sets state=finished for all state=downloaded items.
// Called after a successful import run, or immediately when import is disabled.
func (s *Store) MarkAllDownloadedFinished(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, updated_at = ? WHERE state = ?`,
		StateFinished, time.Now().Unix(), StateDownloaded)
	return err
}

// MarkFailed records a permanent failure at the given stage.
func (s *Store) MarkFailed(ctx context.Context, sngID int64, stage, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, stage = ?, error = ?, in_failed_batch = 0, updated_at = ? WHERE sng_id = ?`,
		StateFailed, stage, errMsg, time.Now().Unix(), sngID)
	return err
}

// MarkInFailedBatch marks items as failed and adds them to the current run's
// failed set (in_failed_batch=1) so the batch can be retried.
func (s *Store) MarkInFailedBatch(ctx context.Context, ids []int64, stage, errMsg string) error {
	now := time.Now().Unix()
	for _, id := range ids {
		_, err := s.db.ExecContext(ctx,
			`UPDATE items SET state = ?, stage = ?, error = ?, in_failed_batch = 1, updated_at = ?
			 WHERE sng_id = ?`,
			StateFailed, stage, errMsg, now, id)
		if err != nil {
			return err
		}
	}
	return nil
}

// ClaimFailedBatch returns all items currently in the failed batch set
// (in_failed_batch=1).
func (s *Store) ClaimFailedBatch(ctx context.Context) ([]*Item, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+itemColumns+` FROM items WHERE in_failed_batch = 1 ORDER BY updated_at ASC`)
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

// RequeueFailedBatch moves the failed batch set back to queued for another
// pass, incrementing batch_attempts and clearing in_failed_batch.
func (s *Store) RequeueFailedBatch(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().Unix()
	placeholders := make([]string, len(ids))
	args := []any{StateQueued, now}
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`UPDATE items SET state = ?, in_failed_batch = 0,
		batch_attempts = batch_attempts + 1, error = '', updated_at = ?
		WHERE sng_id IN (%s)`, strings.Join(placeholders, ","))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// RequeueAllFailed resets all permanently-failed items back to queued,
// clearing batch_attempts and in_failed_batch. Used by `redownload --failed`.
func (s *Store) RequeueAllFailed(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, stage = '', error = '',
			batch_attempts = 0, in_failed_batch = 0, updated_at = ?
		 WHERE state = ?`,
		StateQueued, time.Now().Unix(), StateFailed)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SetState sets an item's state, clearing the error field.
func (s *Store) SetState(ctx context.Context, sngID int64, state string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = '', updated_at = ? WHERE sng_id = ?`,
		state, time.Now().Unix(), sngID)
	return err
}

// SetStateMany sets the state for several tracks at once.
func (s *Store) SetStateMany(ctx context.Context, sngIDs []int64, state string) error {
	for _, id := range sngIDs {
		if err := s.SetState(ctx, id, state); err != nil {
			return err
		}
	}
	return nil
}

// RecoverInterrupted resets in-flight rows after a daemon restart:
// downloading tracks go back to queued. Returns the number of rows recovered.
func (s *Store) RecoverInterrupted(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, updated_at = ? WHERE state = ?`,
		StateQueued, time.Now().Unix(), StateDownloading)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Requeue moves the given tracks back to `queued` for (re)downloading. When
// clearFile is true the recorded file_path is cleared too. Returns the number
// of rows changed.
func (s *Store) Requeue(ctx context.Context, sngIDs []int64, clearFile bool) (int, error) {
	if len(sngIDs) == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	set := `state = ?, stage = '', error = '', batch_attempts = 0, in_failed_batch = 0`
	if clearFile {
		set += `, file_path = ''`
	}
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

// GroupProgress returns (terminal, total) counts for items sharing a group_key.
func (s *Store) GroupProgress(ctx context.Context, groupKey string) (terminal, total int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(CASE WHEN state IN ('finished','failed','skipped','blocklisted') THEN 1 END),
			COUNT(*)
		FROM items WHERE group_key = ?`, groupKey).Scan(&terminal, &total)
	return
}

// SourceProgress returns (terminal, total) counts for items sharing a source.
func (s *Store) SourceProgress(ctx context.Context, sourceType, sourceID string) (terminal, total int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(CASE WHEN state IN ('finished','failed','skipped','blocklisted') THEN 1 END),
			COUNT(*)
		FROM items WHERE source_type = ? AND source_id = ?`, sourceType, sourceID).Scan(&terminal, &total)
	return
}

// FinishedItems returns all items whose state is finished or downloaded.
// Used by `verify` and force-missing.
func (s *Store) FinishedItems(ctx context.Context) ([]*Item, error) {
	return s.List(ctx, []string{StateFinished, StateDownloaded}, 0)
}
