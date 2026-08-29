package cronjob

import (
	"context"
	"time"

	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// repostRetentionBatchLimit stops one tick stalling the cron loop.
const repostRetentionBatchLimit int64 = 500

type repostRetentionLister func(ctx context.Context) ([]db.RepostRetention, error)

type repostEntryDeleter func(ctx context.Context, instanceID int64, before time.Time, limit int64) (int64, error)

// SweepRepostEntries drops rows past their instance's retention window, which
// defaults to forever, so most instances are never listed.
func SweepRepostEntries(ctx context.Context) {
	sweepRepostEntries(ctx, time.Now(), db.ListRepostRetentions, db.DeleteRepostEntriesBefore)
}

// sweepRepostEntries does not let one failing instance abort the rest.
func sweepRepostEntries(
	ctx context.Context,
	now time.Time,
	list repostRetentionLister,
	deleteBefore repostEntryDeleter,
) (deletedRows int64, failedInstances int) {
	retentions, err := list(ctx)
	if err != nil {
		log.Z.Error("failed to list repost retentions", zap.Error(err))
		return 0, 0
	}

	for _, retention := range retentions {
		// Between instances, not mid-instance, so a cancel stays consistent.
		if ctx.Err() != nil {
			break
		}
		if retention.RetentionDays <= 0 {
			// A stored 0 must not read as "delete everything ever posted".
			continue
		}

		cutoff := now.Add(-time.Duration(retention.RetentionDays) * 24 * time.Hour)

		count, err := deleteBefore(ctx, retention.InstanceID, cutoff, repostRetentionBatchLimit)
		if err != nil {
			log.Z.Warn("failed to sweep repost entries for an instance",
				zap.Int64("instance_id", retention.InstanceID), zap.Error(err))
			failedInstances++
			continue
		}

		deletedRows += count
	}

	log.Z.Debug("repost retention sweep finished",
		zap.Int64("deleted", deletedRows), zap.Int("failed", failedInstances))

	return deletedRows, failedInstances
}
