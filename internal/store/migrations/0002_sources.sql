-- Sources are the top-level favorites: albums, artists, and playlists.
-- Tracks (loved tracks) are stored only in items with source_type='track'.
-- Each source is expanded to one item row per track; re-syncing a source
-- re-fetches its track list so newly added tracks are picked up.
CREATE TABLE IF NOT EXISTS sources (
    kind        TEXT    NOT NULL,   -- album|artist|playlist
    ext_id      INTEGER NOT NULL,
    name        TEXT    NOT NULL DEFAULT '',
    -- artist name (for albums; empty for artists/playlists)
    artist      TEXT    NOT NULL DEFAULT '',
    -- waiting: not yet expanded | syncing: being expanded | synced: tracks upserted | failed
    state       TEXT    NOT NULL DEFAULT 'waiting',
    track_count INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (kind, ext_id)
);

CREATE INDEX IF NOT EXISTS idx_sources_state ON sources(state);
