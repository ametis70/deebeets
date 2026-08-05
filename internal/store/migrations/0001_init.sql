-- Items are the unit of work: every favorite (album/artist/playlist/track)
-- expands to one row per track, keyed by the Deezer SNG_ID.
CREATE TABLE IF NOT EXISTS items (
    sng_id          INTEGER PRIMARY KEY,
    title           TEXT    NOT NULL DEFAULT '',
    artist          TEXT    NOT NULL DEFAULT '',
    album           TEXT    NOT NULL DEFAULT '',
    album_artist    TEXT    NOT NULL DEFAULT '',
    -- group_key groups tracks of one release for completion logging.
    group_key       TEXT    NOT NULL DEFAULT '',
    -- how the item entered the queue: track|album|artist|playlist
    source_type     TEXT    NOT NULL DEFAULT '',
    source_id       TEXT    NOT NULL DEFAULT '',
    -- waiting|queued|downloading|downloaded|finished|failed|blocklisted|skipped
    state           TEXT    NOT NULL,
    -- for failed items: download
    stage           TEXT    NOT NULL DEFAULT '',
    format          TEXT    NOT NULL DEFAULT '',
    file_path       TEXT    NOT NULL DEFAULT '',
    -- attempts counts individual download attempts (claim increments this).
    attempts        INTEGER NOT NULL DEFAULT 0,
    -- batch_attempts counts how many full batch-retry passes have been made.
    batch_attempts  INTEGER NOT NULL DEFAULT 0,
    -- in_failed_batch marks items in the current run's failed set so the set
    -- survives a crash and can be retried on resume.
    in_failed_batch INTEGER NOT NULL DEFAULT 0,
    error           TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_items_state ON items(state);
CREATE INDEX IF NOT EXISTS idx_items_group ON items(group_key);
CREATE INDEX IF NOT EXISTS idx_items_failed_batch ON items(in_failed_batch) WHERE in_failed_batch = 1;

-- Blocklisted external IDs. Blocking an album/artist/playlist filters its
-- tracks at sync time; blocking a track also flips any existing item.
CREATE TABLE IF NOT EXISTS blocklist (
    kind       TEXT    NOT NULL,   -- track|album|artist|playlist
    ext_id     INTEGER NOT NULL,
    reason     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (kind, ext_id)
);

-- Free-form daemon state (current_stage, last_sync, rate_limit_until, ...).
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
