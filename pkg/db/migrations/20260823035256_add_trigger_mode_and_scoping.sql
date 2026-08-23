-- Adds trigger.mode, exact-phrase uniqueness, per-instance scoping and the
-- analytics subject column that the trigger matching engine needs.
--
-- trigger_destination is dropped rather than repurposed: it is at
-- destination (channel) grain, but TriggerInstance in trigger.proto scopes a
-- trigger to an INSTANCE (a guild, a room-set), which is a coarser grain.
-- Confirmed by repository search that no Go code has ever read or written
-- trigger_destination, so dropping it loses no data that any code path
-- produced.

-- +goose Up
-- +goose StatementBegin

-- a. trigger.mode. 2 is TRIGGER_MODE_ANY, the default on create.
ALTER TABLE "trigger"
    ADD COLUMN mode int NOT NULL DEFAULT 2;
COMMENT ON COLUMN "trigger".mode IS '0=unspecified, 1=exact, 2=any, 3=regex';

-- b. Exact-mode phrases are globally unique, case-insensitively. A partial
-- index so only exact-mode, live rows are constrained: any-mode and
-- regex-mode phrases may repeat, and a soft-deleted exact phrase frees its
-- text for reuse.
CREATE UNIQUE INDEX uq_trigger_exact_phrase
    ON "trigger" (lower(phrase))
    WHERE mode = 1 AND deleted = FALSE;

-- c. Triggers are scoped per instance, which is the grain TriggerInstance
-- uses. trigger_destination was at the wrong (channel) grain and unused.
DROP TABLE IF EXISTS "trigger_destination";

CREATE TABLE IF NOT EXISTS "trigger_instance"
(
    trigger_id  uuid   NOT NULL,
    instance_id bigint NOT NULL,
    PRIMARY KEY (trigger_id, instance_id),
    CONSTRAINT fk_trigger_instance_trigger FOREIGN KEY (trigger_id)
        REFERENCES "trigger" (id) ON DELETE CASCADE,
    CONSTRAINT fk_trigger_instance_instance FOREIGN KEY (instance_id)
        REFERENCES "instance" (id) ON DELETE CASCADE
);
COMMENT ON TABLE trigger_instance IS 'which instances a trigger is active on';

-- Hot-path index: the per-message lookup is "every active trigger for this
-- instance", run on every message once the compiled-set cache misses.
CREATE INDEX idx_trigger_instance_instance ON "trigger_instance" (instance_id);

-- d. trigger had no index beyond its primary key.
CREATE INDEX idx_trigger_user ON "trigger" (user_id);
CREATE INDEX idx_trigger_file ON "trigger" (file_id);

-- e. Polymorphic subject for analytics (e.g. which trigger fired). No foreign
-- key: the referenced table varies by action_type.
ALTER TABLE "action_record"
    ADD COLUMN subject_id uuid;
COMMENT ON COLUMN action_record.subject_id IS 'uuid of the subject acted on (trigger.id, reminder.id, ...); NULL when the action has no subject. Deliberately has no foreign key: the referenced table varies by action_type.';

CREATE INDEX idx_action_record_subject ON "action_record" (action_type, subject_id);

-- f. Content-hash dedupe needs a real constraint, not just a lookup index.
--
-- If a database somehow already holds two live rows with the same file_hash
-- this aborts with SQLSTATE 23505, and that is the intended behaviour: the two
-- rows have to be reconciled by hand, because deciding which one every
-- referencing row should point at is not a decision a migration can make
-- safely. Collapsing them automatically was tried and rejected — file_hash is
-- only `text NOT NULL`, so rows carrying a sentinel (category 1 is 'metadata,
-- no file data') would group together and get their references rewritten onto
-- an unrelated row.
CREATE UNIQUE INDEX uq_file_hash ON "file" (file_hash) WHERE deleted = FALSE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS uq_file_hash;

DROP INDEX IF EXISTS idx_action_record_subject;
-- Dropping subject_id loses every value written into it: there is nowhere
-- else those associations are recorded. Accepted as a real data loss, not
-- pretended away.
ALTER TABLE "action_record"
    DROP COLUMN IF EXISTS subject_id;

DROP INDEX IF EXISTS idx_trigger_file;
DROP INDEX IF EXISTS idx_trigger_user;

DROP INDEX IF EXISTS idx_trigger_instance_instance;
-- This destroys every trigger_instance row, and those are real data: the new
-- code writes one per scoped instance on every create. After a Down every
-- surviving trigger is scoped to nothing and can never fire again, even if the
-- Up is re-applied.
DROP TABLE IF EXISTS "trigger_instance";

-- Recreated verbatim from 20250105164925_create_tables.sql. It comes back
-- EMPTY: which trigger was scoped to which destination before the drop was
-- never recorded anywhere Go read or wrote, so there is nothing to restore
-- into it. That is lossless only because nothing ever wrote to the original
-- table (verified above); it is not a claim that this Down is fully
-- reversible.
CREATE TABLE IF NOT EXISTS "trigger_destination"
(
    trigger_id     uuid   NOT NULL,
    destination_id bigint NOT NULL,
    PRIMARY KEY (trigger_id, destination_id),
    CONSTRAINT fk_reminder_destination_reminder FOREIGN KEY (trigger_id) REFERENCES "trigger" (id) ON DELETE CASCADE,
    CONSTRAINT fk_reminder_destination_destination FOREIGN KEY (destination_id) REFERENCES "destination" (id) ON DELETE CASCADE
);
COMMENT ON TABLE trigger_destination IS 'composite table for many-to-many relationship between triggers and destinations';

DROP INDEX IF EXISTS uq_trigger_exact_phrase;

-- Dropping trigger.mode loses every stored mode value: there is no other
-- column it can be recovered from.
ALTER TABLE "trigger"
    DROP COLUMN IF EXISTS mode;

-- +goose StatementEnd
