package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lasikuu/GinBot/internal/model"
)

// FileCategoryLocal is file.category for a blob stored on the configured
// storage. The meanings are documented as a COMMENT ON COLUMN in
// 20250105164925_create_tables.sql: 0=unspecified, 1=metadata, 2=local, 3=remote.
const FileCategoryLocal int32 = 2

// GetOrCreateFileByHash returns the id of the file row for a content hash,
// inserting it when it is new. Identical bytes therefore get exactly one row
// and one blob.
//
// inserted reports whether this call created the row, so the caller knows
// whether it must also write the blob.
//
// The upsert targets uq_file_hash, a partial unique index scoped to
// deleted = FALSE, so a soft-deleted file's hash does not block a fresh
// upload from reusing it. DO UPDATE rather than DO NOTHING: DO NOTHING
// returns no row on conflict, so RETURNING would yield nothing for a hash
// that already exists. (xmax = 0) is the idiomatic way to tell an insert
// apart from an update in the same RETURNING clause.
func GetOrCreateFileByHash(
	ctx context.Context,
	hash string,
	path string,
	mimeType string,
	byteSize int32,
) (id string, inserted bool, err error) {
	fileUUID, err := uuid.NewV7()
	if err != nil {
		return "", false, fmt.Errorf("generate file uuid: %w", err)
	}

	err = db().QueryRow(ctx,
		`INSERT INTO file (id, category, path, mime_type, byte_size, file_hash)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (file_hash) WHERE deleted = FALSE
		     DO UPDATE SET file_hash = file.file_hash
		 RETURNING id, (xmax = 0) AS inserted`,
		fileUUID.String(), FileCategoryLocal, path, mimeType, byteSize, hash,
	).Scan(&id, &inserted)
	if err != nil {
		return "", false, fmt.Errorf("get or create file by hash: %w", err)
	}

	return id, inserted, nil
}

// GetFile returns one file row by id. Soft-deleted rows are not returned.
func GetFile(ctx context.Context, id string) (*model.File, error) {
	var file model.File
	err := db().QueryRow(ctx,
		`SELECT `+model.FileColumns+` FROM file WHERE id = $1 AND deleted = FALSE`,
		id,
	).Scan(file.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}

	return &file, nil
}

// ListOrphanFiles returns local file rows that no live trigger references and
// that are older than olderThan, capped at limit.
//
// This exists for the orphan-file GC job a later cycle wires up; the query is
// implemented now, but nothing schedules it yet.
func ListOrphanFiles(ctx context.Context, olderThan time.Time, limit int64) ([]*model.File, error) {
	rows, err := db().Query(ctx,
		`SELECT `+model.FileColumns+`
		 FROM file
		 WHERE category = $1
		   AND deleted = FALSE
		   AND created_at < $2
		   AND NOT EXISTS (
		       SELECT 1 FROM trigger
		        WHERE trigger.file_id = file.id
		          AND trigger.deleted = FALSE
		   )
		 ORDER BY created_at ASC
		 LIMIT $3`,
		FileCategoryLocal, olderThan.UTC(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query orphan files: %w", err)
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		var file model.File
		if err := rows.Scan(file.ScanTargets()...); err != nil {
			return nil, fmt.Errorf("scan orphan file: %w", err)
		}
		files = append(files, &file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orphan files: %w", err)
	}

	return files, nil
}

// SoftDeleteFile marks a file row deleted.
func SoftDeleteFile(ctx context.Context, id string) error {
	tag, err := db().Exec(ctx,
		`UPDATE file SET deleted = TRUE WHERE id = $1 AND deleted = FALSE`,
		id,
	)
	if err != nil {
		return fmt.Errorf("soft delete file: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

// FileVisibleToCaller reports whether fileID is reachable by a caller: it is
// referenced by a live trigger the caller created, or by a live trigger
// scoped to instanceID.
//
// An empty userID or a zero instanceID simply never matches its side of the
// OR (trigger.user_id = NULL and trigger_instance.instance_id = 0 are both
// never true), so a caller with no origin instance still gets the ownership
// check and vice versa, with no special-cased NULL handling needed here.
func FileVisibleToCaller(ctx context.Context, fileID string, userID string, instanceID int64) (bool, error) {
	var visible bool
	err := db().QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM trigger
		     LEFT JOIN trigger_instance ON trigger_instance.trigger_id = trigger.id
		     WHERE trigger.file_id = $1
		       AND trigger.deleted = FALSE
		       AND (trigger.user_id = $2 OR trigger_instance.instance_id = $3)
		 )`,
		fileID, nullStr(userID), instanceID,
	).Scan(&visible)
	if err != nil {
		return false, fmt.Errorf("check file visibility: %w", err)
	}

	return visible, nil
}
