-- Repost detection: repost_entry + repost_fingerprint, dropping the unused
-- legacy "linked" table. Design in docs/plans/wanha.md, ADR-0005, ADR-0008.

-- +goose Up
-- +goose StatementBegin

CREATE TABLE "repost_entry"
(
    id             bigserial   PRIMARY KEY,

    -- Scoped per community: a repost is only a repost where the original was seen.
    instance_id    bigint      NOT NULL REFERENCES "instance" (id) ON DELETE CASCADE,
    destination_id bigint          NULL REFERENCES "destination" (id) ON DELETE SET NULL,
    user_id        uuid            NULL REFERENCES "user_account" (id) ON DELETE SET NULL,

    kind           int         NOT NULL,

    source_key     text            NULL,
    canonical_url  text            NULL,

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

-- One row per hashed region; the MVP writes only region 0 (whole frame). The
-- shape leaves room for tiled crop tolerance and larger hashes later.
CREATE TABLE "repost_fingerprint"
(
    id          bigserial PRIMARY KEY,
    entry_id    bigint   NOT NULL REFERENCES "repost_entry" (id) ON DELETE CASCADE,
    -- Denormalised so the c0..c7 chunk indexes below stay instance-scoped.
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

-- One index per 8-bit chunk. Two hashes within 7 bits share at least one chunk
-- exactly (pigeonhole), so a BitmapOr finds every band match without a scan.
CREATE INDEX idx_rfp_c0 ON "repost_fingerprint" (instance_id, algo, c0);
CREATE INDEX idx_rfp_c1 ON "repost_fingerprint" (instance_id, algo, c1);
CREATE INDEX idx_rfp_c2 ON "repost_fingerprint" (instance_id, algo, c2);
CREATE INDEX idx_rfp_c3 ON "repost_fingerprint" (instance_id, algo, c3);
CREATE INDEX idx_rfp_c4 ON "repost_fingerprint" (instance_id, algo, c4);
CREATE INDEX idx_rfp_c5 ON "repost_fingerprint" (instance_id, algo, c5);
CREATE INDEX idx_rfp_c6 ON "repost_fingerprint" (instance_id, algo, c6);
CREATE INDEX idx_rfp_c7 ON "repost_fingerprint" (instance_id, algo, c7);
CREATE INDEX idx_rfp_entry ON "repost_fingerprint" (entry_id);

-- Unused, and its dHash rows are not migrated to the new pHash index (ADR-0008).
DROP TABLE IF EXISTS "linked";

-- Operator knob set directly in the database; no command or RPC exposes it.
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
DROP TABLE IF EXISTS "repost_fingerprint";

DROP INDEX IF EXISTS idx_repost_posted;
DROP INDEX IF EXISTS idx_repost_content;
DROP INDEX IF EXISTS idx_repost_source;
DROP TABLE IF EXISTS "repost_entry";

ALTER TABLE "instance" DROP COLUMN IF EXISTS repost_retention_days;

-- Recreated empty; "linked" rows were never migrated in. trg_linked_updated_at
-- is deliberately not restored (nothing reads the table).
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
