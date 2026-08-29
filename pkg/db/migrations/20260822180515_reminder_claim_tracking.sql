-- Gives the reminder delivery loop its own clock and attempt counter.
--
-- claimed_at is timestamptz written from Go, so the stale-claim reclaim
-- compares absolute instants. reminder.updated_at can't serve: it is `timestamp
-- without time zone` cast under the unpinned session TimeZone, so a wrong TZ
-- either never reclaims (lost pushes) or reclaims every fresh row (per-second spam).
--
-- delivery_attempts caps a permanently-rejected confirm (owner unregistered on
-- the platform, or clearance dropped) that would otherwise re-post every grace period.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE "reminder"
    ADD COLUMN IF NOT EXISTS claimed_at        timestamptz,
    ADD COLUMN IF NOT EXISTS delivery_attempts int NOT NULL DEFAULT 0;

COMMENT ON COLUMN "reminder".claimed_at IS 'absolute instant the reminder was claimed for delivery (status 2=sent); NULL in every other status. The stale-claim reclaim measures age from here, never from updated_at';
COMMENT ON COLUMN "reminder".delivery_attempts IS 'how many times this reminder has been claimed for delivery without a confirmation sticking; reset on a successful confirm or an owner edit, and capped by maxDeliveryAttempts in pkg/db/reminder.go';

-- Backfill in-flight rows (status 2=sent): a NULL claimed_at is never reclaimed
-- (NULL <= cutoff is never true). NOW() avoids the timezone cast above.
UPDATE "reminder" SET claimed_at = NOW() WHERE status = 2 AND claimed_at IS NULL;

-- Partial index: the reclaim only scans claimed rows.
CREATE INDEX idx_reminder_claimed_at ON "reminder" (claimed_at) WHERE claimed_at IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_reminder_claimed_at;

ALTER TABLE "reminder"
    DROP COLUMN IF EXISTS delivery_attempts,
    DROP COLUMN IF EXISTS claimed_at;

-- +goose StatementEnd
