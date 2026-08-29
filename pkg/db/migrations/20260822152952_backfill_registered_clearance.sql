-- Lifts pre-existing accounts to the clearance floor the interceptor enforces.
-- Rows inserted before db.CreateUser set clearance still hold 0, so they get
-- PermissionDenied everywhere and Register answers AlreadyExists — no self-fix.

-- +goose Up
-- +goose StatementBegin

-- 1 is CLEARANCE_REGISTERED. Soft-deleted rows are left alone (not callers).
UPDATE user_account SET clearance = 1 WHERE clearance = 0 AND deleted = FALSE;

-- Also set in the app on insert; the default guards other insert paths (fixtures, psql).
ALTER TABLE user_account
    ALTER COLUMN clearance SET DEFAULT 1;

COMMENT ON COLUMN user_account.clearance IS '0=unspecified, 1=registered, 10=member, 20=moderator, 50=administrator, 100=owner; matches Clearance in proto/ginbot/proto/user.proto';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE user_account
    ALTER COLUMN clearance SET DEFAULT 0;

-- The backfill is not undone: which rows held 0 before Up is unrecorded, so
-- resetting every 1 back to 0 would demote legitimately-registered accounts.

COMMENT ON COLUMN user_account.clearance IS NULL;

-- +goose StatementEnd
