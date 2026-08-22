-- Lifts pre-existing accounts to the clearance floor the interceptor enforces.
--
-- user_account.clearance was created as `int NOT NULL DEFAULT 0`, and only the
-- later db.CreateUser started setting it explicitly. Every row inserted before
-- that still holds 0 (CLEARANCE_UNSPECIFIED), which is below the minimum every
-- guarded RPC requires, so those users get PermissionDenied on everything —
-- and Register answers AlreadyExists, so they cannot fix it by re-registering.

-- +goose Up
-- +goose StatementBegin

-- 1 is CLEARANCE_REGISTERED in proto/ginbot/proto/user.proto. Soft-deleted rows
-- are left alone: they are not callers, and restoring one should not silently
-- come with a clearance grant.
UPDATE user_account SET clearance = 1 WHERE clearance = 0 AND deleted = FALSE;

-- The application also sets this explicitly on insert. Both, deliberately: any
-- other insert path (a fixture, a manual psql session) would otherwise still be
-- able to create an account that is locked out of every guarded RPC.
ALTER TABLE user_account
    ALTER COLUMN clearance SET DEFAULT 1;

COMMENT ON COLUMN user_account.clearance IS '0=unspecified, 1=registered, 10=member, 20=moderator, 50=administrator, 100=owner; matches Clearance in proto/ginbot/proto/user.proto';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE user_account
    ALTER COLUMN clearance SET DEFAULT 0;

-- The backfill is deliberately not undone. Which rows held 0 before Up ran is
-- not recorded anywhere, so the only available inverse would be to reset every
-- account at 1 back to 0 — which would also demote accounts that were legitimately
-- registered after this migration, locking out users the Up never touched.
-- Losing the distinction is the lesser harm, and it is one-way.

COMMENT ON COLUMN user_account.clearance IS NULL;

-- +goose StatementEnd
