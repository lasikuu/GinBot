-- WANHA repost detection: repost_entry + repost_fingerprint, replacing the
-- unused legacy "linked" table.
--
-- Full design: docs/plans/wanha.md. Decisions W1-W12 and the rejection of
-- pgvector are recorded there and in ADR-0005; ADR-0008 records why "linked"
-- is dropped rather than migrated (its rows are stale, low quality, and
-- built from a different hash algorithm that would poison the new index).

-- +goose Up
-- +goose StatementBegin

CREATE TABLE "repost_entry"
(
    id             bigserial   PRIMARY KEY,

    -- scope (W5): a repost is only a repost within the community that saw
    -- the original. instance_id and user_id carry REAL foreign keys, unlike
    -- the "linked" table this replaces.
    instance_id    bigint      NOT NULL REFERENCES "instance" (id) ON DELETE CASCADE,
    destination_id bigint          NULL REFERENCES "destination" (id) ON DELETE SET NULL,
    user_id        uuid            NULL REFERENCES "user_account" (id) ON DELETE SET NULL,

    kind           int         NOT NULL,

    -- link identity
    source_key     text            NULL,
    canonical_url  text            NULL,

    -- exact binary identity
    file_id        uuid            NULL REFERENCES "file" (id) ON DELETE SET NULL,
    content_hash   bytea           NULL,

    msg_ref        jsonb       NOT NULL,
    posted_at      timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT NOW(),
    updated_at     timestamptz NOT NULL DEFAULT NOW(),

    CONSTRAINT repost_entry_identity_present
        CHECK (source_key IS NOT NULL OR content_hash IS NOT NULL)
);
COMMENT ON TABLE "repost_entry" IS 'links and attachments the community has posted, for WANHA repost detection';
COMMENT ON COLUMN "repost_entry".kind IS '0=unspecified, 1=link, 2=image, 3=video, 4=file';
COMMENT ON COLUMN "repost_entry".file_id IS 'never populated in this MVP: repost detection stores content_hash only and never attachment bytes, since nothing plays a match back and a repost-owned blob would just be swept by the hourly orphan job within the hour. Column kept for a future feature that wants to play the original back.';

CREATE INDEX idx_repost_source  ON "repost_entry" (instance_id, source_key)   WHERE source_key   IS NOT NULL;
CREATE INDEX idx_repost_content ON "repost_entry" (instance_id, content_hash) WHERE content_hash IS NOT NULL;
CREATE INDEX idx_repost_posted  ON "repost_entry" (instance_id, posted_at);

CREATE TRIGGER trg_repost_entry_updated_at BEFORE UPDATE ON "repost_entry" FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Perceptual fingerprints live in a child table (W10), one row per hashed
-- region. The MVP writes exactly one row per image (region = 0, the whole
-- frame); the shape exists so tiled crop tolerance (W11, NOT built here) and
-- larger hash sizes can be added later with no schema migration.
CREATE TABLE "repost_fingerprint"
(
    id          bigserial PRIMARY KEY,
    entry_id    bigint   NOT NULL REFERENCES "repost_entry" (id) ON DELETE CASCADE,
    -- Denormalised deliberately: without instance_id here, the c0..c7 chunk
    -- indexes below could not be scoped per instance and would lose their
    -- selectivity across every guild's traffic at once.
    instance_id bigint   NOT NULL,

    algo        int      NOT NULL,
    region      int      NOT NULL,

    phash       bigint   NOT NULL,
    c0 smallint NOT NULL, c1 smallint NOT NULL, c2 smallint NOT NULL, c3 smallint NOT NULL,
    c4 smallint NOT NULL, c5 smallint NOT NULL, c6 smallint NOT NULL, c7 smallint NOT NULL
);
COMMENT ON TABLE "repost_fingerprint" IS 'perceptual hash fingerprints of repost_entry rows (W2, W3, W10)';
COMMENT ON COLUMN "repost_fingerprint".algo IS '1=phash64';
COMMENT ON COLUMN "repost_fingerprint".region IS '0=whole frame; 1..N reserved for tiled crop-tolerance tiles (W11), not built in this MVP';

-- Eight indexes, one per chunk column. Splitting a 64-bit hash into 8
-- disjoint 8-bit chunks means two hashes differing in at most 7 bit
-- positions are guaranteed to share at least one chunk exactly (pigeonhole
-- principle), so Postgres can find every true match within that band via a
-- BitmapOr across these indexes without ever scanning the whole table.
CREATE INDEX idx_rfp_c0 ON "repost_fingerprint" (instance_id, algo, c0);
CREATE INDEX idx_rfp_c1 ON "repost_fingerprint" (instance_id, algo, c1);
CREATE INDEX idx_rfp_c2 ON "repost_fingerprint" (instance_id, algo, c2);
CREATE INDEX idx_rfp_c3 ON "repost_fingerprint" (instance_id, algo, c3);
CREATE INDEX idx_rfp_c4 ON "repost_fingerprint" (instance_id, algo, c4);
CREATE INDEX idx_rfp_c5 ON "repost_fingerprint" (instance_id, algo, c5);
CREATE INDEX idx_rfp_c6 ON "repost_fingerprint" (instance_id, algo, c6);
CREATE INDEX idx_rfp_c7 ON "repost_fingerprint" (instance_id, algo, c7);
CREATE INDEX idx_rfp_entry ON "repost_fingerprint" (entry_id);

-- "linked" is unused: no Go code reads or writes it, and its rows are
-- deliberately not migrated (ADR-0008) — they are at most 72h old by the old
-- bot's own retention, built from a low-quality dHash, and comparing them
-- against the new pHash algorithm would produce false matches rather than
-- merely missing ones.
DROP TABLE IF EXISTS "linked";

-- Retention (W1): defaults to forever, overridable per instance. There is no
-- settings command or RPC for this column deliberately — it is an operator
-- knob, set directly in the database, not a per-user preference.
ALTER TABLE "instance" ADD COLUMN repost_retention_days int NULL;
COMMENT ON COLUMN "instance".repost_retention_days IS 'days to keep repost_entry rows for this instance; NULL means keep forever (W1), which is the default';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS trg_repost_entry_updated_at ON "repost_entry";

DROP INDEX IF EXISTS idx_rfp_entry;
DROP INDEX IF EXISTS idx_rfp_c7;
DROP INDEX IF EXISTS idx_rfp_c6;
DROP INDEX IF EXISTS idx_rfp_c5;
DROP INDEX IF EXISTS idx_rfp_c4;
DROP INDEX IF EXISTS idx_rfp_c3;
DROP INDEX IF EXISTS idx_rfp_c2;
DROP INDEX IF EXISTS idx_rfp_c1;
DROP INDEX IF EXISTS idx_rfp_c0;
-- Destroys every fingerprint row. They cascade from repost_entry anyway, but
-- dropping the table explicitly here documents that this Down is destructive
-- with rows present, not merely against an empty schema.
DROP TABLE IF EXISTS "repost_fingerprint";

DROP INDEX IF EXISTS idx_repost_posted;
DROP INDEX IF EXISTS idx_repost_content;
DROP INDEX IF EXISTS idx_repost_source;
-- Destroys every entry row: the whole WANHA index. Accepted the same way
-- every other destructive Down in this migration directory is: reversing an
-- applied migration on a live database is expected to lose the data that
-- migration was responsible for.
DROP TABLE IF EXISTS "repost_entry";

ALTER TABLE "instance" DROP COLUMN IF EXISTS repost_retention_days;

-- Recreated from the table definition in 20250105164925_create_tables.sql. It
-- comes back EMPTY: "linked" rows were never migrated in (ADR-0008), so there
-- is nothing to restore into it.
--
-- NOT a byte-for-byte restoration of the pre-Up state: the state this reverses
-- also carried trg_linked_updated_at, added by 20260822120000_fix_schema_defects.sql,
-- and that trigger is not recreated here. Nothing breaks — that migration's own
-- Down drops it with IF EXISTS, and no code reads or writes "linked" at all —
-- but the difference is recorded rather than glossed over.
CREATE TABLE IF NOT EXISTS "linked"
(
    id            BIGSERIAL UNIQUE NOT NULL,
    instance_id   bigint           NOT NULL,
    instance_meta jsonb            NOT NULL,
    category      int              NOT NULL,
    url           text,
    file_id       uuid,
    user_id       uuid,
    deleted       boolean          NOT NULL DEFAULT FALSE,
    created_at    timestamp        NOT NULL DEFAULT NOW(),
    updated_at    timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_linked_file FOREIGN KEY (file_id) REFERENCES "file" (id),
    CONSTRAINT chk_reply_or_file CHECK (url IS NOT NULL OR file_id IS NOT NULL)
);
COMMENT ON TABLE linked IS 'sent links and files to determine if they have already been sent';
COMMENT ON COLUMN "linked".instance_meta IS 'instance specific metadata (user id, msg id, channel id etc.)';
COMMENT ON COLUMN "linked".category IS '0=unspecified, 1=url, 2=file';

CREATE INDEX idx_linked_category ON "linked" (category);

-- +goose StatementEnd
