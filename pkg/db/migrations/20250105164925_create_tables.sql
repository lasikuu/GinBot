-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS "user"
(
    id                    uuid UNIQUE NOT NULL,
    username              text        NOT NULL,
    clearance             int UNIQUE  NOT NULL,
    avatar                text        NOT NULL,
    locale                text,
    timezone              text,
    birthday              timestamp,
    last_congratulated_at timestamp,
    deleted               boolean     NOT NULL DEFAULT FALSE,
    created_at            timestamp   NOT NULL DEFAULT NOW(),
    updated_at            timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS "platform_message"
(
    id            bigserial UNIQUE NOT NULL,
    platform      int              NOT NULL,
    metadata      jsonb            NOT NULL,
    user_id       uuid,
    msg_content   text             NOT NULL,
    msg_timestamp timestamp        NOT NULL,
    deleted       boolean          NOT NULL DEFAULT FALSE,
    created_at    timestamp        NOT NULL DEFAULT NOW(),
    updated_at    timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_platform_message_user FOREIGN KEY (user_id) REFERENCES "user" (id)
);

COMMENT ON COLUMN "platform_message".platform IS 'enum value of the platform the message was sent from';
COMMENT ON COLUMN "platform_message".metadata IS 'metadata of the message including the msg_id, channel_id etc.';
COMMENT ON COLUMN "platform_message".user_id IS 'internal user_id of the msg author if available';

CREATE TABLE IF NOT EXISTS "file"
(
    id         uuid UNIQUE NOT NULL,
    path       timestamp   NOT NULL,
    mime_type  text        NOT NULL,
    byte_size  int         NOT NULL,
    file_hash  text        NOT NULL,
    deleted    boolean     NOT NULL DEFAULT FALSE,
    created_at timestamp   NOT NULL DEFAULT NOW(),
    updated_at timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS "platform_attachment"
(
    id              bigserial UNIQUE NOT NULL,
    platform_msg_id bigint           NOT NULL,
    file_id         uuid,
    file_url        text,
    deleted         boolean          NOT NULL DEFAULT FALSE,
    created_at      timestamp        NOT NULL DEFAULT NOW(),
    updated_at      timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_platform_attachment_msg FOREIGN KEY (platform_msg_id) REFERENCES "platform_message" (id) ON DELETE CASCADE,
    CONSTRAINT fk_platform_attachment_file FOREIGN KEY (file_id) REFERENCES "file" (id)
);

CREATE TABLE IF NOT EXISTS "reminder"
(
    id         uuid UNIQUE NOT NULL,
    datetime   timestamp   NOT NULL,
    timezone   text UNIQUE NOT NULL,
    status     int         NOT NULL DEFAULT 0,
    message    text,
    user_id    uuid,
    file_id    uuid,
    parent_id  int,
    deleted    boolean     NOT NULL DEFAULT FALSE,
    created_at timestamp   NOT NULL DEFAULT NOW(),
    updated_at timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_reminder_user FOREIGN KEY (user_id) REFERENCES "user" (id) ON DELETE CASCADE,
    CONSTRAINT fk_reminder_file FOREIGN KEY (file_id) REFERENCES "file" (id)
);

CREATE INDEX idx_reminder_datetime ON reminder (datetime);
CREATE INDEX idx_reminder_status ON reminder (status);

CREATE TABLE IF NOT EXISTS "reminder_destination"
(
    id          bigserial UNIQUE NOT NULL,
    reminder_id uuid             NOT NULL,
    platform    int              NOT NULL,
    platform_id text             NOT NULL,
    priority    int              NOT NULL,
    deleted     boolean          NOT NULL DEFAULT FALSE,
    created_at  timestamp        NOT NULL DEFAULT NOW(),
    updated_at  timestamp        NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_rem_dest_reminder FOREIGN KEY (reminder_id) REFERENCES "reminder" (id) ON DELETE CASCADE
);

CREATE INDEX idx_reminder_destination_platform ON "reminder_destination" (platform);

CREATE TABLE IF NOT EXISTS "trigger"
(
    id         uuid UNIQUE NOT NULL,
    phrase     text        NOT NULL,
    reply      text        NOT NULL,
    file_id    uuid,
    user_id    uuid,
    parent_id  int,
    deleted    boolean     NOT NULL DEFAULT FALSE,
    created_at timestamp   NOT NULL DEFAULT NOW(),
    updated_at timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_trigger_file FOREIGN KEY (file_id) REFERENCES "file" (id),
    CONSTRAINT fk_trigger_user FOREIGN KEY (user_id) REFERENCES "user" (id)
);

CREATE TABLE IF NOT EXISTS "highlight"
(
    id              uuid UNIQUE NOT NULL,
    original_msg_id bigint      NOT NULL,
    copy_msg_id     bigint,
    deleted         boolean     NOT NULL DEFAULT FALSE,
    created_at      timestamp   NOT NULL DEFAULT NOW(),
    updated_at      timestamp   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT fk_highlight_original_msg_id FOREIGN KEY (original_msg_id) REFERENCES "platform_message" (id),
    CONSTRAINT fk_highlight_copy_msg_id FOREIGN KEY (copy_msg_id) REFERENCES "platform_message" (id)
);

COMMENT ON COLUMN "highlight".original_msg_id IS 'id of the highlighted message';
COMMENT ON COLUMN "highlight".copy_msg_id IS 'id of the highlighted message copy';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_rem_dest_platform;
DROP INDEX IF EXISTS idx_rem_status;
DROP INDEX IF EXISTS idx_rem_datetime;

DROP TABLE IF EXISTS "highlight";
DROP TABLE IF EXISTS "trigger";
DROP TABLE IF EXISTS "reminder_destination";
DROP TABLE IF EXISTS "reminder";
DROP TABLE IF EXISTS "file";
DROP TABLE IF EXISTS "platform_message";
DROP TABLE IF EXISTS "user";
-- +goose StatementEnd
