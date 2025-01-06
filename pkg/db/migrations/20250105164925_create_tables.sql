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

CREATE TABLE IF NOT EXISTS "instance"
(
    id              bigserial UNIQUE NOT NULL,
    platform_enum   int              NOT NULL,
    instance_meta   jsonb            NOT NULL,
    default_channel text,
    deleted         boolean          NOT NULL DEFAULT FALSE,
    created_at      timestamp        NOT NULL DEFAULT NOW(),
    updated_at      timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
COMMENT ON TABLE instance IS 'instances linked to the bot';
COMMENT ON COLUMN instance.platform_enum IS '0=unknown, 1=matrix protocol, 2=discord, 3=telegram, 4=line, 5=email, 6=snailmail';
COMMENT ON COLUMN instance.instance_meta IS 'instance specific metadata (server id etc.)';
COMMENT ON COLUMN instance.default_channel IS 'default communication channel for the instance for announcements etc.';

CREATE INDEX idx_instance_platform_enum ON instance (platform_enum);

CREATE TABLE IF NOT EXISTS "platform_user"
(
    id            bigserial UNIQUE NOT NULL,
    user_id       uuid             NOT NULL,
    platform_enum int              NOT NULL,
    platform_uid  text             NOT NULL,
    user_meta     jsonb,
    deleted       boolean          NOT NULL DEFAULT FALSE,
    created_at    timestamp        NOT NULL DEFAULT NOW(),
    updated_at    timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_user_account FOREIGN KEY (user_id) REFERENCES "user_account" (id),
    CONSTRAINT unique_platform_user_enum_uid UNIQUE (platform_enum, platform_uid)
);
COMMENT ON TABLE platform_user IS 'platform specific user data';
COMMENT ON COLUMN "platform_user".platform_uid IS 'id of the user on the platform';
COMMENT ON COLUMN "platform_user".user_meta IS 'platform specific metadata (display name, roles etc.)';

CREATE INDEX idx_platform_user_platform_enum ON "platform_user" (platform_enum);

CREATE TABLE IF NOT EXISTS "message"
(
    id            bigserial UNIQUE NOT NULL,
    instance_id   bigint           NOT NULL,
    msg_meta      jsonb            NOT NULL,
    msg_content   text             NOT NULL,
    msg_timestamp timestamp        NOT NULL,
    user_id       uuid,
    deleted       boolean          NOT NULL DEFAULT FALSE,
    created_at    timestamp        NOT NULL DEFAULT NOW(),
    updated_at    timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_message_instance FOREIGN KEY (instance_id) REFERENCES "instance" (id),
    CONSTRAINT fk_message_user FOREIGN KEY (user_id) REFERENCES "user_account" (id)
);
COMMENT ON TABLE message IS 'messages on any platform';
COMMENT ON COLUMN message.msg_meta IS 'metadata of the message including the uid msg_id, channel_id etc.';
COMMENT ON COLUMN message.user_id IS 'internal user_id of the msg author if available';

CREATE INDEX idx_message_instance ON "message" (instance_id);

CREATE TABLE IF NOT EXISTS "message_attachment"
(
    id         bigserial UNIQUE NOT NULL,
    message_id bigint           NOT NULL,
    file_id    uuid,
    file_url   text,
    deleted    boolean          NOT NULL DEFAULT FALSE,
    created_at timestamp        NOT NULL DEFAULT NOW(),
    updated_at timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_message_attachment_msg FOREIGN KEY (message_id) REFERENCES "message" (id) ON DELETE CASCADE,
    CONSTRAINT fk_message_attachment_file FOREIGN KEY (file_id) REFERENCES "file" (id)
);
COMMENT ON TABLE message_attachment IS 'attachments of a message';

CREATE TABLE IF NOT EXISTS "destination"
(
    id          bigserial UNIQUE NOT NULL,
    instance_id bigint           NOT NULL,
    meta        jsonb            NOT NULL,
    deleted     boolean          NOT NULL DEFAULT FALSE,
    created_at  timestamp        NOT NULL DEFAULT NOW(),
    updated_at  timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_destination_instance FOREIGN KEY (instance_id) REFERENCES "instance" (id) ON DELETE CASCADE
);
COMMENT ON TABLE destination IS 'message destinations (channels, rooms etc.)';

CREATE INDEX idx_destination_instance ON "destination" (instance_id);

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
    CONSTRAINT fk_highlight_original_msg FOREIGN KEY (original_mid) REFERENCES "message" (id),
    CONSTRAINT fk_highlight_copy_msg FOREIGN KEY (copy_mid) REFERENCES "message" (id)
);
COMMENT ON TABLE highlight IS 'highlighted or pinned messages';
COMMENT ON COLUMN "highlight".original_mid IS 'id of the original message';
COMMENT ON COLUMN "highlight".copy_mid IS 'id of the message copy (e.g. message copied to a special channel)';

CREATE TABLE IF NOT EXISTS "linked"
(
    id            BIGSERIAL UNIQUE NOT NULL,
    instance_id   bigint           NOT NULL,
    instance_meta jsonb            NOT NULL,
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
COMMENT ON COLUMN "linked".instance_meta IS 'instance specific metadata (user id, msg id, channel id etc.)';
COMMENT ON COLUMN "linked".category IS '0=unknown, 1=url, 2=file';

CREATE INDEX idx_linked_category ON "linked" (category);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_linked_category;
DROP INDEX IF EXISTS idx_reminder_status;
DROP INDEX IF EXISTS idx_reminder_datetime;
DROP INDEX IF EXISTS idx_destination_instance;
DROP INDEX IF EXISTS idx_message_instance;
DROP INDEX IF EXISTS idx_platform_user_platform;
DROP INDEX IF EXISTS idx_instance_platform_enum;
DROP INDEX IF EXISTS idx_file_category;

DROP TABLE IF EXISTS "linked";
DROP TABLE IF EXISTS "highlight";
DROP TABLE IF EXISTS "trigger_destination";
DROP TABLE IF EXISTS "trigger";
DROP TABLE IF EXISTS "destination_preference";
DROP TABLE IF EXISTS "reminder";
DROP TABLE IF EXISTS "destination";
DROP TABLE IF EXISTS "message_attachment";
DROP TABLE IF EXISTS "message";
DROP TABLE IF EXISTS "platform_user";
DROP TABLE IF EXISTS "instance";
DROP TABLE IF EXISTS "user_account";
DROP TABLE IF EXISTS "file";
-- +goose StatementEnd
