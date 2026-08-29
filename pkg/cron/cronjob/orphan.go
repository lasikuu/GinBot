package cronjob

import (
	"context"
	"time"

	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/storage"
	"go.uber.org/zap"
)

// orphanGracePeriod spares a blob stored just before its trigger row commits.
const orphanGracePeriod = 1 * time.Hour

// orphanBatchLimit bounds one sweep so a single tick cannot stall the cron loop.
const orphanBatchLimit int64 = 100

type orphanLister func(ctx context.Context, olderThan time.Time, limit int64) ([]*model.File, error)

type orphanDeleter func(ctx context.Context, id string) error

// CollectOrphanFiles reclaims file rows no live trigger references (ADR-0007).
func CollectOrphanFiles(ctx context.Context) {
	collectOrphanFiles(ctx, time.Now(), db.ListOrphanFiles, db.SoftDeleteFile, storage.Default())
}

// collectOrphanFiles soft-deletes the row BEFORE its blob: a row left behind
// makes every later sweep re-list the same file.
func collectOrphanFiles(
	ctx context.Context,
	now time.Time,
	list orphanLister,
	softDelete orphanDeleter,
	blobs storage.Storage,
) (deleted int, failed int) {
	files, err := list(ctx, now.Add(-orphanGracePeriod), orphanBatchLimit)
	if err != nil {
		log.Z.Error("failed to list orphan files", zap.Error(err))
		return 0, 0
	}

	// Logged once per sweep rather than once per file.
	if blobs == nil && len(files) > 0 {
		log.Z.Error("blob storage is not configured; orphan rows will be soft-deleted without removing their blobs",
			zap.Int("orphans", len(files)))
	}

	for _, file := range files {
		// Between rows, not mid-row, so a cancelled sweep stays consistent.
		if ctx.Err() != nil {
			break
		}
		if file == nil {
			continue
		}

		if err := softDelete(ctx, file.ID); err != nil {
			// No path or hash: both leak where user-supplied media landed.
			log.Z.Warn("failed to soft-delete an orphan file row",
				zap.String("file_id", file.ID), zap.Error(err))
			failed++
			continue
		}

		// The row is gone regardless, but an uncollected blob is not a success.
		if blobs == nil {
			failed++
			continue
		}
		if err := blobs.Delete(ctx, file.Path); err != nil {
			log.Z.Warn("failed to delete an orphan file blob",
				zap.String("file_id", file.ID), zap.Error(err))
			failed++
			continue
		}

		deleted++
	}

	log.Z.Debug("orphan file sweep finished",
		zap.Int("deleted", deleted), zap.Int("failed", failed))

	return deleted, failed
}
