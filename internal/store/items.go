package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Item is a queue row: one Deezer track and its pipeline state.
type Item struct {
	SngID         int64
	Title         string
	Artist        string
	Album         string
	AlbumArtist   string
	GroupKey      string
	SourceType    string
	SourceID      string
	State         string
	Stage         string // legacy column kept for schema compat
	Format        string
	FilePath      string
	Attempts      int
	BatchAttempts int
	InFailedBatch bool
	Error         string
	TrackData     string // JSON: song.getData response
	LyricsData    string // JSON: song.getLyrics response (empty if none)
	DeezerStatus  string // PRESENT | REPLACED | MISSING
	ReplacementID int64  // SNG_ID of the replacement track (0 if none)
	CreatedAt     int64
	UpdatedAt     int64
}

// Discovered is the metadata a sync knows about a track before downloading.
type Discovered struct {
	SngID         int64
	Title         string
	Artist        string
	Album         string
	AlbumArtist   string
	GroupKey      string
	SourceType    string
	SourceID      string
	TrackData     string // JSON blob from song.getData
	LyricsData    string // JSON blob from song.getLyrics
	DeezerStatus  string // PRESENT | REPLACED | MISSING
	ReplacementID int64  // SNG_ID of the replacement track (0 if none)
}

const itemColumns = `sng_id, title, artist, album, album_artist, group_key,
	source_type, source_id, state, stage, format, file_path,
	attempts, batch_attempts, in_failed_batch, error, track_data, lyrics_data,
	deezer_status, replacement_id, created_at, updated_at`

func scanItem(row interface{ Scan(...any) error }) (*Item, error) {
	var it Item
	var inFailed int
	err := row.Scan(&it.SngID, &it.Title, &it.Artist, &it.Album, &it.AlbumArtist,
		&it.GroupKey, &it.SourceType, &it.SourceID, &it.State, &it.Stage,
		&it.Format, &it.FilePath,
		&it.Attempts, &it.BatchAttempts, &inFailed,
		&it.Error, &it.TrackData, &it.LyricsData,
		&it.DeezerStatus, &it.ReplacementID,
		&it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		return nil, err
	}
	it.InFailedBatch = inFailed == 1
	return &it, nil
}

// Upsert records a discovered track. New tracks are inserted as `waiting`
// (or `blocklisted` if blocked). On re-sync, metadata and cached API data are
// refreshed but state/progress are preserved. Returns true if inserted.
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

	deezerStatus := d.DeezerStatus
	if deezerStatus == "" {
		deezerStatus = DeezerStatusPresent
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO items (`+itemColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', 0, 0, 0, '', ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sng_id) DO NOTHING`,
		d.SngID, d.Title, d.Artist, d.Album, d.AlbumArtist, d.GroupKey,
		d.SourceType, d.SourceID, initial,
		d.TrackData, d.LyricsData, deezerStatus, d.ReplacementID, now, now)
	if err != nil {
		return false, err
	}
	if affected, _ := res.RowsAffected(); affected == 1 {
		return true, nil
	}

	// Existing row: refresh metadata and cached API data, preserving state/progress.
	_, err = s.db.ExecContext(ctx, `
		UPDATE items SET title = ?, artist = ?, album = ?, album_artist = ?,
			group_key = ?, source_type = ?, source_id = ?,
			track_data = ?, lyrics_data = ?,
			deezer_status = ?, replacement_id = ?, updated_at = ?
		WHERE sng_id = ?`,
		d.Title, d.Artist, d.Album, d.AlbumArtist, d.GroupKey,
		d.SourceType, d.SourceID, d.TrackData, d.LyricsData,
		deezerStatus, d.ReplacementID, now, d.SngID)
	return false, err
}

// SyncReplacedState copies the state, format, and file_path from a replacement
// item to the REPLACED original, so both reflect the same pipeline progress.
// Called after upserting a REPLACED item when its replacement already exists.
func (s *Store) SyncReplacedState(ctx context.Context, replacedSngID, replacementSngID int64) error {
	rep, err := s.Get(ctx, replacementSngID)
	if err != nil || rep == nil || rep.State == StateWaiting || rep.State == StateQueued {
		return nil // replacement hasn't progressed yet; nothing to copy
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE items SET state = ?, format = ?, file_path = ?, updated_at = ?
		WHERE sng_id = ?`,
		rep.State, rep.Format, rep.FilePath, time.Now().Unix(), replacedSngID)
	return err
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

// CountByDeezerStatus returns the number of items per deezer_status.
func (s *Store) CountByDeezerStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT deezer_status, COUNT(*) FROM items GROUP BY deezer_status`)
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

// ListByDeezerStatus returns items with the given deezer_status, optionally
// further filtered by state. Newest first.
func (s *Store) ListByDeezerStatus(ctx context.Context, deezerStatus string, states []string, limit int) ([]*Item, error) {
	q := `SELECT ` + itemColumns + ` FROM items WHERE deezer_status = ?`
	args := []any{deezerStatus}
	if len(states) > 0 {
		placeholders := make([]string, len(states))
		for i, st := range states {
			placeholders[i] = "?"
			args = append(args, st)
		}
		q += ` AND state IN (` + strings.Join(placeholders, ",") + `)`
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

// UpdateTrackData refreshes the cached track_data JSON for an existing item.
func (s *Store) UpdateTrackData(ctx context.Context, sngID int64, trackData string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET track_data = ?, updated_at = ? WHERE sng_id = ?`,
		trackData, time.Now().Unix(), sngID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CopyStateToReplacement copies the pipeline state (state, format, file_path)
// from an original item to its replacement. Called when a replacement track is
// first synced and the original has already been downloaded.
func (s *Store) CopyStateToReplacement(ctx context.Context, originalSngID, replacementSngID int64) error {
	original, err := s.Get(ctx, originalSngID)
	if err != nil || original == nil || original.FilePath == "" {
		return nil // nothing to copy
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE items SET state = ?, format = ?, file_path = ?, updated_at = ?
		WHERE sng_id = ?`,
		original.State, original.Format, original.FilePath,
		time.Now().Unix(), replacementSngID)
	return err
}

// ClaimDownload atomically claims the oldest pending PRESENT track for downloading,
// incrementing its attempt counter. REPLACED and MISSING items are skipped.
// Returns (nil, false, nil) when the queue is empty.
func (s *Store) ClaimDownload(ctx context.Context) (*Item, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE items SET state = ?, stage = '', attempts = attempts + 1, updated_at = ?
		WHERE sng_id = (
			SELECT sng_id FROM items
			WHERE state IN (?, ?) AND (deezer_status = ? OR deezer_status = '')
			ORDER BY updated_at ASC LIMIT 1
		)
		RETURNING `+itemColumns,
		StateDownloading, time.Now().Unix(),
		StateWaiting, StateQueued, DeezerStatusPresent)
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

// ClaimTag atomically claims the oldest downloaded track for tagging.
func (s *Store) ClaimTag(ctx context.Context) (*Item, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE items SET state = ?, updated_at = ?
		WHERE sng_id = (
			SELECT sng_id FROM items WHERE state = ?
			ORDER BY updated_at ASC LIMIT 1
		)
		RETURNING `+itemColumns,
		StateTagging, time.Now().Unix(), StateDownloaded)
	it, err := scanItem(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return it, true, nil
}

// MarkTagged moves an item to `tagged` (tagging succeeded).
func (s *Store) MarkTagged(ctx context.Context, sngID int64) error {
	return s.SetState(ctx, sngID, StateTagged)
}

// MarkConverted moves an item to `converted` (terminal success).
func (s *Store) MarkConverted(ctx context.Context, sngID int64) error {
	return s.SetState(ctx, sngID, StateConverted)
}

// MarkFailedDownload records a permanent download failure.
func (s *Store) MarkFailedDownload(ctx context.Context, sngID int64, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = ?, in_failed_batch = 0, updated_at = ? WHERE sng_id = ?`,
		StateFailedDownload, errMsg, time.Now().Unix(), sngID)
	return err
}

// MarkFailedTag records a tagging failure.
func (s *Store) MarkFailedTag(ctx context.Context, sngID int64, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = ?, updated_at = ? WHERE sng_id = ?`,
		StateFailedTag, errMsg, time.Now().Unix(), sngID)
	return err
}

// MarkFailedConvert records a conversion failure.
func (s *Store) MarkFailedConvert(ctx context.Context, sngID int64, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = ?, updated_at = ? WHERE sng_id = ?`,
		StateFailedConvert, errMsg, time.Now().Unix(), sngID)
	return err
}

// MarkInFailedBatch marks items as failed_download and adds them to the current
// run's failed set (in_failed_batch=1) so the batch can be retried.
func (s *Store) MarkInFailedBatch(ctx context.Context, ids []int64, errMsg string) error {
	now := time.Now().Unix()
	for _, id := range ids {
		_, err := s.db.ExecContext(ctx,
			`UPDATE items SET state = ?, error = ?, in_failed_batch = 1, updated_at = ?
			 WHERE sng_id = ?`,
			StateFailedDownload, errMsg, now, id)
		if err != nil {
			return err
		}
	}
	return nil
}

// MarkFailed is a compatibility shim; prefer the typed MarkFailed* methods.
func (s *Store) MarkFailed(ctx context.Context, sngID int64, stage, errMsg string) error {
	switch stage {
	case "convert":
		return s.MarkFailedConvert(ctx, sngID, errMsg)
	case "tag":
		return s.MarkFailedTag(ctx, sngID, errMsg)
	default:
		return s.MarkFailedDownload(ctx, sngID, errMsg)
	}
}

// ClaimFailedBatch returns all items currently in the failed batch set.
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

// RequeueAllFailed resets all permanently-failed items back to queued.
// Used by `redownload --failed`.
func (s *Store) RequeueAllFailed(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = '',
			batch_attempts = 0, in_failed_batch = 0, updated_at = ?
		 WHERE state IN (?, ?, ?)`,
		StateQueued, time.Now().Unix(),
		StateFailedDownload, StateFailedTag, StateFailedConvert)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RequeueAllFailedDownloads resets only download-failed items back to queued.
func (s *Store) RequeueAllFailedDownloads(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = '',
			batch_attempts = 0, in_failed_batch = 0, updated_at = ?
		 WHERE state = ?`,
		StateQueued, time.Now().Unix(), StateFailedDownload)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RequeueAllFailedTags resets only tag-failed items back to downloaded for retry.
func (s *Store) RequeueAllFailedTags(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = '', updated_at = ? WHERE state = ?`,
		StateDownloaded, time.Now().Unix(), StateFailedTag)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RequeueAllFailedConverts resets only convert-failed items back to tagged for retry.
func (s *Store) RequeueAllFailedConverts(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = '', updated_at = ? WHERE state = ?`,
		StateTagged, time.Now().Unix(), StateFailedConvert)
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

// RecoverInterrupted resets in-flight rows after a daemon restart.
// downloading → queued, tagging → downloaded, converting → tagged.
func (s *Store) RecoverInterrupted(ctx context.Context) (int, error) {
	now := time.Now().Unix()
	total := 0
	for _, pair := range [][2]string{
		{StateDownloading, StateQueued},
		{StateTagging, StateDownloaded},
		{StateConverting, StateTagged},
	} {
		res, err := s.db.ExecContext(ctx,
			`UPDATE items SET state = ?, updated_at = ? WHERE state = ?`,
			pair[1], now, pair[0])
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// Requeue moves the given tracks back to `queued` for (re)downloading. When
// clearFile is true the recorded file_path is cleared too.
func (s *Store) Requeue(ctx context.Context, sngIDs []int64, clearFile bool) (int, error) {
	if len(sngIDs) == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	set := `state = ?, error = '', batch_attempts = 0, in_failed_batch = 0`
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

// RequeueForRetag moves tagged/converted items back to downloaded so the tag
// stage will re-process them. Used by `retag --all`.
func (s *Store) RequeueForRetag(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET state = ?, error = '', updated_at = ?
		 WHERE state IN (?, ?, ?)`,
		StateDownloaded, time.Now().Unix(),
		StateTagged, StateConverted, StateFailedTag)
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
			COUNT(CASE WHEN state IN ('tagged','converted','failed_download','failed_tag','failed_convert','skipped','blocklisted') THEN 1 END),
			COUNT(*)
		FROM items WHERE group_key = ?`, groupKey).Scan(&terminal, &total)
	return
}

// SourceProgress returns (terminal, total) counts for items sharing a source.
func (s *Store) SourceProgress(ctx context.Context, sourceType, sourceID string) (terminal, total int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(CASE WHEN state IN ('tagged','converted','failed_download','failed_tag','failed_convert','skipped','blocklisted') THEN 1 END),
			COUNT(*)
		FROM items WHERE source_type = ? AND source_id = ?`, sourceType, sourceID).Scan(&terminal, &total)
	return
}

// FinishedItems returns all items that have a file on disk (tagged or converted).
// Used by verify and force-missing.
func (s *Store) FinishedItems(ctx context.Context) ([]*Item, error) {
	return s.List(ctx, []string{StateTagged, StateConverted, StateDownloaded}, 0)
}

// CountFailedByStage returns counts of failed items grouped by failure state.
func (s *Store) CountFailedByStage(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT state, COUNT(*) FROM items
		 WHERE state IN (?, ?, ?)
		 GROUP BY state`,
		StateFailedDownload, StateFailedTag, StateFailedConvert)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}
