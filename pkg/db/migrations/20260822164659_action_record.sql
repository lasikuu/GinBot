-- Adds the action_record table that backs analytics writes. The first users are
-- the reminder paths: a REMINDER_CREATED row on create and a REMINDER_DELIVERED
-- row on a confirmed delivery.
--
-- No repeat-interval column is added to reminder: repeat_cron is the only repeat
-- mechanism, and sub-minute intervals are expressed losslessly as `@every 90s`,
-- so a separate seconds column would be redundant (decision D-B).

-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS "action_record"
(
    id               bigserial   NOT NULL,
    action_type      int         NOT NULL,
    action_timestamp timestamptz NOT NULL DEFAULT NOW(),
    action_time      bigint,
    actor_id         uuid,
    PRIMARY KEY (id),
    CONSTRAINT fk_action_record_actor FOREIGN KEY (actor_id) REFERENCES "user_account" (id) ON DELETE SET NULL
);
COMMENT ON TABLE action_record IS 'recorded actions for analytics (reminders delivered, triggers occurred etc.)';
-- Kept in sync with proto/ginbot/proto/analytics.proto ActionType.
COMMENT ON COLUMN action_record.action_type IS '0=unspecified, 1=trigger occurred, 2=trigger called, 3=reminder created, 4=reminder delivered, 5=squad rallied, 6=squad formed';
COMMENT ON COLUMN action_record.action_time IS 'time taken to perform the action in milliseconds, nullable';
COMMENT ON COLUMN action_record.actor_id IS 'user_account.id of the actor if known; NULL when the actor is deleted or unknown';

CREATE INDEX idx_action_record_type_ts ON "action_record" (action_type, action_timestamp);
CREATE INDEX idx_action_record_actor ON "action_record" (actor_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reversible with data present: dropping the indexes and then the table removes
-- exactly what Up added, regardless of how many rows exist.
DROP INDEX IF EXISTS idx_action_record_actor;
DROP INDEX IF EXISTS idx_action_record_type_ts;
DROP TABLE IF EXISTS "action_record";

-- +goose StatementEnd
