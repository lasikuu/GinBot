-- Adds a short, human-typeable reference number as an alias for the UUID
-- primary key on trigger and reminder. See ADR-0039.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE "trigger" ADD COLUMN ref bigint;
UPDATE "trigger" t SET ref = s.rn
  FROM (SELECT id, row_number() OVER (ORDER BY created_at, id) AS rn FROM "trigger") s
  WHERE t.id = s.id;
CREATE SEQUENCE trigger_ref_seq OWNED BY "trigger".ref;
SELECT setval('trigger_ref_seq', COALESCE((SELECT MAX(ref) FROM "trigger"), 0) + 1, false);
ALTER TABLE "trigger" ALTER COLUMN ref SET DEFAULT nextval('trigger_ref_seq');
ALTER TABLE "trigger" ALTER COLUMN ref SET NOT NULL;
CREATE UNIQUE INDEX idx_trigger_ref ON "trigger" (ref);
COMMENT ON COLUMN "trigger".ref IS 'display/input alias for id, global and never reused (a soft-deleted trigger keeps its ref); not an identity, and never stored elsewhere';

ALTER TABLE "reminder" ADD COLUMN ref bigint;
UPDATE "reminder" r SET ref = s.rn
  FROM (SELECT id, row_number() OVER (PARTITION BY user_id ORDER BY created_at, id) AS rn FROM "reminder") s
  WHERE r.id = s.id;
ALTER TABLE "reminder" ALTER COLUMN ref SET NOT NULL;
-- Per-user, not global. user_id is nullable and NULLs are distinct here, so
-- unowned rows are unconstrained; nothing resolves a ref for one. See ADR-0039.
CREATE UNIQUE INDEX idx_reminder_user_ref ON "reminder" (user_id, ref);
COMMENT ON COLUMN "reminder".ref IS 'display/input alias for id, unique per user_id, allocated under the same advisory lock as the active-reminder cap; not an identity, and never stored elsewhere';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_reminder_user_ref;
ALTER TABLE "reminder" DROP COLUMN IF EXISTS ref;

DROP INDEX IF EXISTS idx_trigger_ref;
ALTER TABLE "trigger" ALTER COLUMN ref DROP DEFAULT;
DROP SEQUENCE IF EXISTS trigger_ref_seq;
ALTER TABLE "trigger" DROP COLUMN IF EXISTS ref;

-- +goose StatementEnd
