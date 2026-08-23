package cronjob

import (
	"context"
	"time"

	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// repostRetentionBatchLimit bounds one sweep, per instance, so a single tick
// cannot stall the cron loop. A backlog beyond this drains over several
// ticks rather than in one long one, mirroring orphanBatchLimit.
const repostRetentionBatchLimit int64 = 500

// repostRetentionLister lists instances with a finite retention window.
// It exists so the sweep can be exercised without a database.
type repostRetentionLister func(ctx context.Context) ([]db.RepostRetention, error)

// repostEntryDeleter removes entries posted before a cutoff, capped at limit,
// reporting how many went.
type repostEntryDeleter func(ctx context.Context, instanceID int64, before time.Time, limit int64) (int64, error)

// SweepRepostEntries drops repost_entry rows past their instance's retention
// window.
//
// Retention defaults to forever (W1): an instance with no
// repost_retention_days set is never even listed here, so most deployments
// never sweep anything and unlimited memory is exactly the point of the
// feature.
func SweepRepostEntries(ctx context.Context) {
	sweepRepostEntries(ctx, time.Now(), db.ListRepostRetentions, db.DeleteRepostEntriesBefore)
}

// sweepRepostEntries is the injectable core of the sweep, mirroring
// collectOrphanFiles's shape: a lowercase core taking now plus function-typed
// dependencies, returning counts and logging rather than propagating errors.
//
// deletedRows counts rows actually removed across every instance swept;
// failedInstances counts instances whose delete call itself failed. One
// instance failing must not abort the rest of the sweep, the same
// "a partial failure is not the whole job's failure" property
// collectOrphanFiles has for its per-file failures.
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
		// Between instances, not mid-instance: a cancelled sweep leaves
		// consistent state and the next tick picks the rest up.
		if ctx.Err() != nil {
			break
		}
		if retention.RetentionDays <= 0 {
			// Defensive: db.ListRepostRetentions already filters NULL out at
			// the SQL level, but a zero or negative value stored directly by
			// an operator must not be read as "delete everything ever
			// posted".
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
