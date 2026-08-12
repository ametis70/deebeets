-- Migration 0004: track Deezer availability status at the item and source level.
--
-- deezer_status values:
--   PRESENT  — track/album is available on Deezer (STATUS=1)
--   REPLACED — track has been replaced by a new ID (STATUS=3, FALLBACK present)
--   MISSING  — track is unavailable with no known replacement
--
-- replacement_id — SNG_ID (items) or ALB_ID (sources) of the replacement.

ALTER TABLE items   ADD COLUMN deezer_status  TEXT    NOT NULL DEFAULT 'PRESENT';
ALTER TABLE items   ADD COLUMN replacement_id INTEGER NOT NULL DEFAULT 0;

ALTER TABLE sources ADD COLUMN deezer_status  TEXT    NOT NULL DEFAULT 'PRESENT';
ALTER TABLE sources ADD COLUMN replacement_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_items_deezer_status   ON items(deezer_status);
CREATE INDEX IF NOT EXISTS idx_sources_deezer_status ON sources(deezer_status);
