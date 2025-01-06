-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS "file"
(
    id         uuid UNIQUE NOT NULL,
    category   int         NOT NULL DEFAULT 0,
    path       timestamp   NOT NULL,
    mime_type  text        NOT NULL,
    byte_size  int         NOT NULL,
    file_hash  text        NOT NULL,
    deleted    boolean     NOT NULL DEFAULT FALSE,
    created_at timestamp   NOT NULL DEFAULT NOW(),
    updated_at timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
COMMENT ON TABLE file IS 'represents a file in the system';
COMMENT ON COLUMN "file".category IS '0=unknown, 1=metadata (no file data), 2=local file, 3=remote file';

CREATE INDEX idx_file_category ON "file" (category);

CREATE TABLE IF NOT EXISTS "user_account"
(
    id                    uuid UNIQUE NOT NULL,
    username              text        NOT NULL,
    clearance             int         NOT NULL DEFAULT 0,
    avatar                text,
    locale                text,
    timezone              text,
    birthday              timestamp,
    last_congratulated_at timestamp,
    deleted               boolean     NOT NULL DEFAULT FALSE,
    created_at            timestamp   NOT NULL DEFAULT NOW(),
    updated_at            timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
COMMENT ON TABLE user_account IS 'internal user accounts';

CREATE TABLE IF NOT EXISTS "platform"
(
    id              bigserial UNIQUE NOT NULL,
    platform_enum   int              NOT NULL,
    platform_meta   jsonb            NOT NULL,
    default_channel text,
    deleted         boolean          NOT NULL DEFAULT FALSE,
    created_at      timestamp        NOT NULL DEFAULT NOW(),
    updated_at      timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
COMMENT ON TABLE platform IS 'platforms linked to the bot';
COMMENT ON COLUMN "platform".platform_enum IS '0=unknown, 1=matrix protocol, 2=discord, 3=telegram, 4=line, 5=email, 6=snailmail';
COMMENT ON COLUMN "platform".platform_meta IS 'platform specific metadata (server id etc.)';
COMMENT ON COLUMN "platform".default_channel IS 'default communication channel for the platform for announcements etc.';

CREATE INDEX idx_platform_category ON "platform" (platform_enum);

CREATE TABLE IF NOT EXISTS "platform_user"
(
    id            bigserial UNIQUE NOT NULL,
    platform_id   bigint           NOT NULL,
    platform_uid  text             NOT NULL,
    platform_meta jsonb,
    user_id       uuid,
    deleted       boolean          NOT NULL DEFAULT FALSE,
    created_at    timestamp        NOT NULL DEFAULT NOW(),
    updated_at    timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_platform_user_platform FOREIGN KEY (platform_id) REFERENCES "platform" (id),
    CONSTRAINT fk_platform_message_user FOREIGN KEY (user_id) REFERENCES "user_account" (id)
);
COMMENT ON TABLE platform_user IS 'platform specific user data';
COMMENT ON COLUMN "platform_user".platform_id IS 'id of the platform';
COMMENT ON COLUMN "platform_user".platform_uid IS 'id of the user on the platform';
COMMENT ON COLUMN "platform_user".platform_meta IS 'platform specific metadata (display name, roles etc.)';

CREATE INDEX idx_platform_user_platform ON "platform_user" (platform_id);

CREATE TABLE IF NOT EXISTS "platform_message"
(
    id            bigserial UNIQUE NOT NULL,
    platform_id   bigint           NOT NULL,
    platform_meta jsonb            NOT NULL,
    msg_content   text             NOT NULL,
    msg_timestamp timestamp        NOT NULL,
    user_id       uuid,
    deleted       boolean          NOT NULL DEFAULT FALSE,
    created_at    timestamp        NOT NULL DEFAULT NOW(),
    updated_at    timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_platform_message_platform FOREIGN KEY (platform_id) REFERENCES "platform" (id),
    CONSTRAINT fk_platform_message_user FOREIGN KEY (user_id) REFERENCES "user_account" (id)
);
COMMENT ON TABLE platform_message IS 'messages on any platform';
COMMENT ON COLUMN "platform_message".platform_meta IS 'metadata of the message including the msg id, channel id etc.';
COMMENT ON COLUMN "platform_message".user_id IS 'internal user_id of the msg author if available';

CREATE INDEX idx_platform_message_platform ON "platform_message" (platform_id);

CREATE TABLE IF NOT EXISTS "platform_attachment"
(
    id                  bigserial UNIQUE NOT NULL,
    platform_message_id bigint           NOT NULL,
    file_id             uuid,
    file_url            text,
    deleted             boolean          NOT NULL DEFAULT FALSE,
    created_at          timestamp        NOT NULL DEFAULT NOW(),
    updated_at          timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_platform_attachment_message FOREIGN KEY (platform_message_id) REFERENCES "platform_message" (id) ON DELETE CASCADE,
    CONSTRAINT fk_platform_attachment_file FOREIGN KEY (file_id) REFERENCES "file" (id)
);
COMMENT ON TABLE platform_attachment IS 'attachments of a message';

CREATE TABLE IF NOT EXISTS "destination"
(
    id            bigserial UNIQUE NOT NULL,
    platform_id   bigint           NOT NULL,
    platform_meta jsonb            NOT NULL,
    deleted       boolean          NOT NULL DEFAULT FALSE,
    created_at    timestamp        NOT NULL DEFAULT NOW(),
    updated_at    timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_destination_platform FOREIGN KEY (platform_id) REFERENCES "platform" (id) ON DELETE CASCADE
);
COMMENT ON TABLE destination IS 'message destinations (channels, rooms etc.)';

CREATE INDEX idx_destination_platform ON "destination" (platform_id);

CREATE TABLE IF NOT EXISTS "reminder"
(
    id             uuid UNIQUE NOT NULL,
    datetime       timestamp   NOT NULL,
    timezone       text        NOT NULL,
    repeat_cron    text,
    destination_id bigint      NOT NULL,
    status         int         NOT NULL DEFAULT 0,
    user_id        uuid,
    message        text,
    parent_id      int,
    deleted        boolean     NOT NULL DEFAULT FALSE,
    created_at     timestamp   NOT NULL DEFAULT NOW(),
    updated_at     timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_reminder_destination FOREIGN KEY (destination_id) REFERENCES "destination" (id),
    CONSTRAINT fk_reminder_user FOREIGN KEY (user_id) REFERENCES "user_account" (id) ON DELETE CASCADE
);
COMMENT ON TABLE reminder IS 'reminders set by users';
COMMENT ON COLUMN "reminder".status IS '0=pending, 1=sent, 2=delivered, 3=failed';

CREATE INDEX idx_reminder_datetime ON reminder (datetime);
CREATE INDEX idx_reminder_status ON reminder (status);

CREATE TABLE IF NOT EXISTS "destination_preference"
(
    user_id        uuid    NOT NULL,
    destination_id bigint  NOT NULL,
    priority       int     NOT NULL DEFAULT 0,
    urgent         boolean NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_id, destination_id),
    CONSTRAINT fk_destination_pref_user FOREIGN KEY (user_id) REFERENCES "user_account" (id) ON DELETE CASCADE,
    CONSTRAINT fk_destination_pref_reminder FOREIGN KEY (destination_id) REFERENCES "destination" (id) ON DELETE CASCADE
);
COMMENT ON TABLE destination_preference IS 'user preferences for message destinations (reminders, notifications etc.)';
COMMENT ON COLUMN "destination_preference".priority IS 'reminders are sent to lower value priority destinations first';
COMMENT ON COLUMN "destination_preference".urgent IS 'destination for notifications marked as urgent';

CREATE TABLE IF NOT EXISTS "trigger"
(
    id         uuid UNIQUE NOT NULL,
    phrase     text        NOT NULL,
    reply      text,
    file_id    uuid,
    user_id    uuid,
    chance     int         NOT NULL DEFAULT 100 CHECK (chance >= 0 AND chance <= 100),
    deleted    boolean     NOT NULL DEFAULT FALSE,
    created_at timestamp   NOT NULL DEFAULT NOW(),
    updated_at timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_trigger_file FOREIGN KEY (file_id) REFERENCES "file" (id),
    CONSTRAINT fk_trigger_user FOREIGN KEY (user_id) REFERENCES "user_account" (id),
    CONSTRAINT chk_reply_or_file CHECK (reply IS NOT NULL OR file_id IS NOT NULL)
);
COMMENT ON TABLE trigger IS 'triggers for the bot';

CREATE TABLE IF NOT EXISTS "trigger_destination"
(
    trigger_id     uuid   NOT NULL,
    destination_id bigint NOT NULL,
    PRIMARY KEY (trigger_id, destination_id),
    CONSTRAINT fk_reminder_destination_reminder FOREIGN KEY (trigger_id) REFERENCES "trigger" (id) ON DELETE CASCADE,
    CONSTRAINT fk_reminder_destination_destination FOREIGN KEY (destination_id) REFERENCES "destination" (id) ON DELETE CASCADE
);
COMMENT ON TABLE trigger_destination IS 'composite table for many-to-many relationship between triggers and destinations';

CREATE TABLE IF NOT EXISTS "highlight"
(
    id           uuid UNIQUE NOT NULL,
    original_mid bigint      NOT NULL,
    copy_mid     bigint,
    deleted      boolean     NOT NULL DEFAULT FALSE,
    created_at   timestamp   NOT NULL DEFAULT NOW(),
    updated_at   timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_highlight_original_msg FOREIGN KEY (original_mid) REFERENCES "platform_message" (id),
    CONSTRAINT fk_highlight_copy_msg FOREIGN KEY (copy_mid) REFERENCES "platform_message" (id)
);
COMMENT ON TABLE highlight IS 'highlighted or pinned messages';
COMMENT ON COLUMN "highlight".original_mid IS 'id of the original message';
COMMENT ON COLUMN "highlight".copy_mid IS 'id of the message copy (e.g. message copied to a special channel)';

CREATE TABLE IF NOT EXISTS "linked"
(
    id            BIGSERIAL UNIQUE NOT NULL,
    platform_id   bigint           NOT NULL,
    platform_meta jsonb            NOT NULL,
    category      int              NOT NULL,
    url           text,
    file_id       uuid,
    user_id       uuid,
    deleted       boolean          NOT NULL DEFAULT FALSE,
    created_at    timestamp        NOT NULL DEFAULT NOW(),
    updated_at    timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_linked_file FOREIGN KEY (file_id) REFERENCES "file" (id),
    CONSTRAINT chk_reply_or_file CHECK (url IS NOT NULL OR file_id IS NOT NULL)
);
COMMENT ON TABLE linked IS 'sent links and files to determine if they have already been sent';
COMMENT ON COLUMN "linked".platform_meta IS 'platform specific metadata (user id, msg id, channel id etc.)';
COMMENT ON COLUMN "linked".category IS '0=unknown, 1=url, 2=file';

CREATE INDEX idx_linked_category ON "linked" (category);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_linked_category;
DROP INDEX IF EXISTS idx_reminder_status;
DROP INDEX IF EXISTS idx_reminder_datetime;
DROP INDEX IF EXISTS idx_destination_platform;
DROP INDEX IF EXISTS idx_platform_message_platform;
DROP INDEX IF EXISTS idx_platform_user_platform;
DROP INDEX IF EXISTS idx_platform_category;
DROP INDEX IF EXISTS idx_file_category;

DROP TABLE IF EXISTS "linked";
DROP TABLE IF EXISTS "highlight";
DROP TABLE IF EXISTS "trigger_destination";
DROP TABLE IF EXISTS "trigger";
DROP TABLE IF EXISTS "destination_preference";
DROP TABLE IF EXISTS "reminder";
DROP TABLE IF EXISTS "destination";
DROP TABLE IF EXISTS "platform_attachment";
DROP TABLE IF EXISTS "platform_message";
DROP TABLE IF EXISTS "platform_user";
DROP TABLE IF EXISTS "platform";
DROP TABLE IF EXISTS "user_account";
DROP TABLE IF EXISTS "file";
-- +goose StatementEnd
