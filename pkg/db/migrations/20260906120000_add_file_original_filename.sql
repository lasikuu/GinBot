-- +goose Up
-- +goose StatementBegin

ALTER TABLE "file" ADD COLUMN original_filename text NOT NULL DEFAULT '';
COMMENT ON COLUMN "file".original_filename IS 'display name for the blob, from the upload source; empty when unknown. Written sanitised by the server, verbatim by cmd/ginbot-migrate, so a reader must not assume it is safe to render unescaped';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE "file" DROP COLUMN IF EXISTS original_filename;

-- +goose StatementEnd
