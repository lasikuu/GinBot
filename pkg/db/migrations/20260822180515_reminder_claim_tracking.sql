-- Gives the reminder delivery loop its own clock and its own attempt counter.
--
-- WHY claimed_at, when reminder.updated_at already moves on every UPDATE:
-- updated_at is `timestamp without time zone` written by the
-- trg_reminder_updated_at trigger as NOW(). That timestamptz -> timestamp cast
-- uses the SESSION TimeZone, and pkg/db builds its DSN with no TimeZone
-- parameter, so the stored wall clock is whatever the server happens to be set
-- to. The stale-claim reclaim compares that column against a cutoff computed in
-- Go as now().UTC() - grace, and both ways of being wrong fail silently:
--
--   * session TZ ahead of UTC -> updated_at looks like the future -> the reclaim
--     never fires and every dropped push is lost forever;
--   * session TZ behind UTC   -> updated_at looks hours old -> every freshly
--     claimed row is reclaimed on the next tick and immediately re-claimed, so
--     one reminder is delivered once per second until a confirm wins.
--
-- claimed_at is timestamptz and is written explicitly from Go, so both sides of
-- the comparison are absolute instants and neither depends on an unpinned
-- server setting.
--
-- WHY delivery_attempts: a confirm can be rejected permanently rather than lost
-- (the owner has no platform_user row for the platform, so the outgoing call
-- carries no user_id and the clearance interceptor answers InvalidArgument
-- forever; or the owner's clearance dropped below REGISTERED). The channel post
-- succeeds every cycle in that case, so the user is spammed once per grace
-- period indefinitely and the reminder never reaches a terminal status. Counting
-- attempts lets the reclaim give up and mark the reminder FAILED instead.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE "reminder"
    ADD COLUMN IF NOT EXISTS claimed_at        timestamptz,
    ADD COLUMN IF NOT EXISTS delivery_attempts int NOT NULL DEFAULT 0;

COMMENT ON COLUMN "reminder".claimed_at IS 'absolute instant the reminder was claimed for delivery (status 2=sent); NULL in every other status. The stale-claim reclaim measures age from here, never from updated_at';
COMMENT ON COLUMN "reminder".delivery_attempts IS 'how many times this reminder has been claimed for delivery without a confirmation sticking; reset on a successful confirm or an owner edit, and capped by maxDeliveryAttempts in pkg/db/reminder.go';

-- Backfill any reminder already in flight (status 2 = sent, see the status
-- comment on this table). Stamping NOW() rather than deriving an instant from
-- updated_at deliberately avoids the timezone cast this migration exists to get
-- away from: the worst case is that an already-stuck reminder waits one further
-- grace period, whereas a row left with claimed_at NULL would never be reclaimed
-- at all because NULL <= cutoff is never true.
UPDATE "reminder" SET claimed_at = NOW() WHERE status = 2 AND claimed_at IS NULL;

-- Partial index: the reclaim only ever scans claimed rows, which is a tiny
-- fraction of the table.
CREATE INDEX idx_reminder_claimed_at ON "reminder" (claimed_at) WHERE claimed_at IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reversible with data present: dropping the index and then the two columns
-- removes exactly what Up added, however many reminder rows exist. The values
-- held in those columns are lost, which is correct — they did not exist before
-- Up — and no other column is touched, so no reminder loses its schedule,
-- message, owner or status.
DROP INDEX IF EXISTS idx_reminder_claimed_at;

ALTER TABLE "reminder"
    DROP COLUMN IF EXISTS delivery_attempts,
    DROP COLUMN IF EXISTS claimed_at;

-- +goose StatementEnd
