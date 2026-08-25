-- +goose Up
-- +goose StatementBegin
ALTER TABLE "reminder"
    ADD COLUMN IF NOT EXISTS legacy_id bigint;

COMMENT ON COLUMN "reminder".legacy_id IS 'original AUTO_INCREMENT id from the legacy MySQL reminders table; NULL for natively created reminders';

-- Partial unique index: enforce uniqueness only among imported rows
CREATE UNIQUE INDEX idx_reminder_legacy_id ON "reminder" (legacy_id) WHERE legacy_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_reminder_legacy_id;
ALTER TABLE "reminder" DROP COLUMN IF EXISTS legacy_id;
-- +goose StatementEnd
