-- One-time backfill of file.original_filename from the legacy on-disk names
-- cmd/ginbot-migrate recorded in migration_id_map (entity = 'file'). Only for a
-- database migrated before cmd/ginbot-migrate started writing the column
-- itself; a fresh migrate run needs nothing here. migration_id_map is a scratch
-- table that tool creates, not a goose migration, which is why this is hand-run.
--
-- Idempotent: only touches rows with no original_filename yet, and DISTINCT ON
-- with a fixed ORDER BY picks the same legacy name on every re-run when several
-- legacy names deduped onto one file row.
--
-- Names are stored verbatim, unlike the server's sanitised write path, and the
-- UPDATE is table-wide.
--
-- Run with:
--   psql "$GINBOT_DSN" -f cmd/ginbot-migrate/backfill_original_filename.sql

UPDATE "file"
SET original_filename = picked.legacy_id
FROM (
    SELECT DISTINCT ON (new_id) new_id, legacy_id
    FROM migration_id_map
    WHERE entity = 'file'
    ORDER BY new_id, legacy_id
) AS picked
WHERE "file".id = picked.new_id::uuid
  AND "file".original_filename = '';
