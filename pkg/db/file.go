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

// FileCategoryLocal is file.category for a blob on the configured storage.
const FileCategoryLocal int32 = 2

// GetOrCreateFileByHash reports inserted so the caller knows whether it must
// also write the blob. The upsert targets uq_file_hash, a partial index on
// deleted = FALSE; DO UPDATE because DO NOTHING returns no row for RETURNING.
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

// GetFilesByIDs is GetFile for many files in one query, keyed by id. An id
// absent from the result was not found or was soft-deleted.
func GetFilesByIDs(ctx context.Context, fileIDs []string) (map[string]*model.File, error) {
	out := make(map[string]*model.File, len(fileIDs))
	if len(fileIDs) == 0 {
		return out, nil
	}

	rows, err := db().Query(ctx,
		`SELECT `+model.FileColumns+` FROM file WHERE id = ANY($1) AND deleted = FALSE`,
		fileIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("query files batch: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var file model.File
		if err := rows.Scan(file.ScanTargets()...); err != nil {
			return nil, fmt.Errorf("scan file batch: %w", err)
		}
		out[file.ID] = &file
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate files batch: %w", err)
	}

	return out, nil
}

// ListOrphanFiles returns local files no live trigger references. olderThan is a
// grace period so a blob written just before its trigger commits is not swept.
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

// FileVisibleToCaller reports whether a live trigger the caller created, or one
// scoped to instanceID, references fileID. An empty userID or a zero instanceID
// never matches its side of the OR, so neither needs special-casing.
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
