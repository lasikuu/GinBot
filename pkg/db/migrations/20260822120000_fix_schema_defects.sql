-- Corrects defects in 20250105164925_create_tables.sql.
-- See docs/plans/mvp.md "Schema defects".

-- +goose Up
-- +goose StatementBegin

-- file.path was declared `timestamp` but holds a storage path.
-- The column is unused so far, so a plain type change is safe.
ALTER TABLE "file"
    ALTER COLUMN path TYPE text USING path::text;
COMMENT ON COLUMN "file".path IS 'storage path or key, relative to the configured storage root';

-- reminder.parent_id referenced reminder.id (a uuid) but was declared `int`,
-- so the self-reference could never work and no FK could be created.
ALTER TABLE "reminder"
    ALTER COLUMN parent_id TYPE uuid USING NULL;
ALTER TABLE "reminder"
    ADD CONSTRAINT fk_reminder_parent FOREIGN KEY (parent_id) REFERENCES "reminder" (id) ON DELETE SET NULL;
COMMENT ON COLUMN "reminder".parent_id IS 'reminder this one was copied from, for subscribe-to-reminder';

CREATE INDEX idx_reminder_parent ON "reminder" (parent_id) WHERE parent_id IS NOT NULL;

-- The platform enum comments were inverted relative to proto/ginbot/proto/platform.proto,
-- which defines 1=DISCORD and 2=MATRIX_PROTOCOL.
COMMENT ON COLUMN instance.platform_enum IS '0=unspecified, 1=discord, 2=matrix protocol, 3=telegram, 4=line, 5=email, 6=snailmail';
COMMENT ON COLUMN platform_user.platform_enum IS '0=unspecified, 1=discord, 2=matrix protocol, 3=telegram, 4=line, 5=email, 6=snailmail';

-- updated_at columns defaulted to NOW() on insert but never advanced on UPDATE.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_file_updated_at BEFORE UPDATE ON "file" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_user_account_updated_at BEFORE UPDATE ON "user_account" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_instance_updated_at BEFORE UPDATE ON "instance" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_platform_user_updated_at BEFORE UPDATE ON "platform_user" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_message_updated_at BEFORE UPDATE ON "message" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_message_attachment_updated_at BEFORE UPDATE ON "message_attachment" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_destination_updated_at BEFORE UPDATE ON "destination" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_reminder_updated_at BEFORE UPDATE ON "reminder" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_trigger_updated_at BEFORE UPDATE ON "trigger" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_highlight_updated_at BEFORE UPDATE ON "highlight" FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_linked_updated_at BEFORE UPDATE ON "linked" FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS trg_linked_updated_at ON "linked";
DROP TRIGGER IF EXISTS trg_highlight_updated_at ON "highlight";
DROP TRIGGER IF EXISTS trg_trigger_updated_at ON "trigger";
DROP TRIGGER IF EXISTS trg_reminder_updated_at ON "reminder";
DROP TRIGGER IF EXISTS trg_destination_updated_at ON "destination";
DROP TRIGGER IF EXISTS trg_message_attachment_updated_at ON "message_attachment";
DROP TRIGGER IF EXISTS trg_message_updated_at ON "message";
DROP TRIGGER IF EXISTS trg_platform_user_updated_at ON "platform_user";
DROP TRIGGER IF EXISTS trg_instance_updated_at ON "instance";
DROP TRIGGER IF EXISTS trg_user_account_updated_at ON "user_account";
DROP TRIGGER IF EXISTS trg_file_updated_at ON "file";

DROP FUNCTION IF EXISTS set_updated_at();

COMMENT ON COLUMN instance.platform_enum IS '0=unknown, 1=matrix protocol, 2=discord, 3=telegram, 4=line, 5=email, 6=snailmail';
COMMENT ON COLUMN platform_user.platform_enum IS NULL;

DROP INDEX IF EXISTS idx_reminder_parent;
ALTER TABLE "reminder"
    DROP CONSTRAINT IF EXISTS fk_reminder_parent;
ALTER TABLE "reminder"
    ALTER COLUMN parent_id TYPE int USING NULL;

ALTER TABLE "file"
    ALTER COLUMN path TYPE timestamp USING NULL;

-- +goose StatementEnd
