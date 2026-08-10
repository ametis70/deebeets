-- Migration 0003: separate tagging and conversion into distinct states;
-- add track_data and lyrics_data JSON caches to items;
-- add album_cache table for album.getData responses.

-- Add JSON cache columns to items.
ALTER TABLE items ADD COLUMN track_data  TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN lyrics_data TEXT NOT NULL DEFAULT '';

-- Rename terminal/failure states for existing rows.
-- finished      → converted   (was: download+import done, now: full pipeline done)
-- failed (stage=download) → failed_download
-- failed (stage=convert)  → failed_convert
UPDATE items SET state = 'converted'       WHERE state = 'finished';
UPDATE items SET state = 'failed_download' WHERE state = 'failed' AND stage = 'download';
UPDATE items SET state = 'failed_convert'  WHERE state = 'failed' AND stage = 'convert';
UPDATE items SET state = 'failed_download' WHERE state = 'failed' AND stage = '';

-- Album metadata cache keyed by Deezer ALB_ID.
-- Populated at sync time; shared across all tracks that reference the album.
CREATE TABLE IF NOT EXISTS album_cache (
    alb_id     INTEGER PRIMARY KEY,
    data       TEXT    NOT NULL DEFAULT '',  -- JSON: album.getData response
    updated_at INTEGER NOT NULL
);
