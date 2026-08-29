-- Adds trigger.mode, exact-phrase uniqueness, per-instance scoping and the
-- analytics subject column. trigger_destination is dropped (wrong, channel
-- grain; unused) in favour of instance-grained scoping.

-- +goose Up
-- +goose StatementBegin

-- Default 2 is TRIGGER_MODE_ANY.
ALTER TABLE "trigger"
    ADD COLUMN mode int NOT NULL DEFAULT 2;
COMMENT ON COLUMN "trigger".mode IS '0=unspecified, 1=exact, 2=any, 3=regex';

-- Partial index: only live exact-mode phrases are globally unique (any/regex
-- may repeat; a soft-deleted exact phrase frees its text).
CREATE UNIQUE INDEX uq_trigger_exact_phrase
    ON "trigger" (lower(phrase))
    WHERE mode = 1 AND deleted = FALSE;

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

-- Hot path: "every active trigger for this instance", run on every cache miss.
CREATE INDEX idx_trigger_instance_instance ON "trigger_instance" (instance_id);

CREATE INDEX idx_trigger_user ON "trigger" (user_id);
CREATE INDEX idx_trigger_file ON "trigger" (file_id);

-- Polymorphic subject; no FK because the referenced table varies by action_type.
ALTER TABLE "action_record"
    ADD COLUMN subject_id uuid;
COMMENT ON COLUMN action_record.subject_id IS 'uuid of the subject acted on (trigger.id, reminder.id, ...); NULL when the action has no subject. Deliberately has no foreign key: the referenced table varies by action_type.';

CREATE INDEX idx_action_record_subject ON "action_record" (action_type, subject_id);

-- Aborts with 23505 on pre-existing live duplicates by design: they must be
-- reconciled by hand, not auto-collapsed (sentinel hashes would merge unrelated rows).
CREATE UNIQUE INDEX uq_file_hash ON "file" (file_hash) WHERE deleted = FALSE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS uq_file_hash;

DROP INDEX IF EXISTS idx_action_record_subject;
-- Destructive: subject_id values are recorded nowhere else.
ALTER TABLE "action_record"
    DROP COLUMN IF EXISTS subject_id;

DROP INDEX IF EXISTS idx_trigger_file;
DROP INDEX IF EXISTS idx_trigger_user;

DROP INDEX IF EXISTS idx_trigger_instance_instance;
-- Destructive: after this every surviving trigger is scoped to nothing.
DROP TABLE IF EXISTS "trigger_instance";

-- Recreated empty; the original scoping was never written by any code.
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

-- Destructive: mode values are recoverable from no other column.
ALTER TABLE "trigger"
    DROP COLUMN IF EXISTS mode;

-- +goose StatementEnd
