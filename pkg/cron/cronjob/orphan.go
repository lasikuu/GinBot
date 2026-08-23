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

// orphanGracePeriod keeps a freshly written file out of the sweep, so a blob
// stored seconds before its trigger row commits is not collected.
const orphanGracePeriod = 1 * time.Hour

// orphanBatchLimit bounds one sweep so a single tick cannot stall the cron loop.
const orphanBatchLimit int64 = 100

// orphanLister lists collectable file rows. It exists so the sweep can be
// exercised without a database.
type orphanLister func(ctx context.Context, olderThan time.Time, limit int64) ([]*model.File, error)

// orphanDeleter soft-deletes one file row.
type orphanDeleter func(ctx context.Context, id string) error

// CollectOrphanFiles sweeps file rows that no live trigger references.
//
// Trigger media is fetched and stored before the trigger row that references it
// exists, and a failed create or update leaves the blob behind deliberately —
// a compensating delete could remove a blob another trigger deduped onto
// (ADR-0007). This is where those blobs are reclaimed instead.
func CollectOrphanFiles(ctx context.Context) {
	collectOrphanFiles(ctx, time.Now(), db.ListOrphanFiles, db.SoftDeleteFile, storage.Default())
}

// collectOrphanFiles is the injectable core of the sweep.
//
// The row is soft-deleted BEFORE its blob, which is the opposite of the
// intuitive order and is deliberate: a failed blob delete must not leave the row
// behind, because the row is what makes the next sweep re-list the same file
// forever. Storage.Delete already treats a missing blob as success, so the
// reverse ordering buys nothing and costs progress.
//
// deleted counts files FULLY collected — row and blob both gone. A row whose
// blob delete failed counts as failed and not as deleted, so the two numbers
// partition the batch and the debug line says how much work actually completed
// rather than double-counting a half-finished file.
//
// Known narrow race, accepted rather than closed: db.GetOrCreateFileByHash
// conflicts only on deleted = FALSE, so identical bytes re-uploaded in the
// window between this sweep's soft delete and its blob delete get a NEW row
// pointing at a blob this sweep then removes. The grace period does not help —
// it bounds the age of the row being collected, not the gap between the two
// deletes. Against an hourly job the window is milliseconds wide, and the
// outcome is a failed GetFile rather than corruption or a wrong file being
// served, so it is not worth a transaction spanning the blob store.
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

	// A nil store is survivable and still worth sweeping: the rows are soft
	// deleted anyway, because leaving them would make every later sweep re-list
	// the same files and never make progress. The blobs stay on disk and this
	// says so once per sweep rather than once per file.
	if blobs == nil && len(files) > 0 {
		log.Z.Error("blob storage is not configured; orphan rows will be soft-deleted without removing their blobs",
			zap.Int("orphans", len(files)))
	}

	for _, file := range files {
		// Between rows, not mid-row: a cancelled sweep leaves consistent state
		// and the next tick picks the rest up.
		if ctx.Err() != nil {
			break
		}
		if file == nil {
			continue
		}

		if err := softDelete(ctx, file.ID); err != nil {
			// No path and no hash in the log line: neither says anything the id
			// does not, and both leak where user-supplied media landed.
			log.Z.Warn("failed to soft-delete an orphan file row",
				zap.String("file_id", file.ID), zap.Error(err))
			failed++
			continue
		}

		// The row is gone from here on regardless. Only a clean blob delete
		// makes the file fully collected, so an unconfigured store counts every
		// row it skipped as failed — the blobs really are still on disk.
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
