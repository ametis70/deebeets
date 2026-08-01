-- Items are the unit of work: every favorite (album/artist/playlist/track)
-- expands to one row per track, keyed by the Deezer SNG_ID.
CREATE TABLE IF NOT EXISTS items (
    sng_id       INTEGER PRIMARY KEY,
    title        TEXT    NOT NULL DEFAULT '',
    artist       TEXT    NOT NULL DEFAULT '',
    album        TEXT    NOT NULL DEFAULT '',
    album_artist TEXT    NOT NULL DEFAULT '',
    -- group_key groups tracks of one release so beets can import per album.
    group_key    TEXT    NOT NULL DEFAULT '',
    -- how the item entered the queue: track|album|artist|playlist
    source_type  TEXT    NOT NULL DEFAULT '',
    source_id    TEXT    NOT NULL DEFAULT '',
    -- waiting|queued|downloading|downloaded|import_queued|importing|
    -- finished|failed|blocklisted|skipped
    state        TEXT    NOT NULL,
    -- for failed items: download|import
    stage        TEXT    NOT NULL DEFAULT '',
    format       TEXT    NOT NULL DEFAULT '',
    file_path    TEXT    NOT NULL DEFAULT '',
    tmp_path     TEXT    NOT NULL DEFAULT '',
    bytes_done   INTEGER NOT NULL DEFAULT 0,
    attempts     INTEGER NOT NULL DEFAULT 0,
    error        TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_items_state ON items(state);
CREATE INDEX IF NOT EXISTS idx_items_group ON items(group_key);

-- Blocklisted external IDs. Blocking an album/artist/playlist filters its
-- tracks at sync time; blocking a track also flips any existing item.
CREATE TABLE IF NOT EXISTS blocklist (
    kind       TEXT    NOT NULL,   -- track|album|artist|playlist
    ext_id     INTEGER NOT NULL,
    reason     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (kind, ext_id)
);

-- Free-form daemon state (run status, last sync, rate-limit info, ...).
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
