package cronjob

import (
	"context"
	"time"

	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/reminder"
	"go.uber.org/zap"
)

// Remind runs once per cron tick. It reclaims reminders whose delivery was lost,
// claims every reminder now due, and pushes each to its platform client over the
// reverse stream.
//
// Double delivery is prevented by the claim itself: db.ClaimDueReminders flips
// PENDING -> SENT atomically, so a reminder pushed on this tick is already SENT
// before the next tick's claim runs and cannot be claimed twice. A push that is
// dropped (the reverse stream drops on a full client buffer) leaves the reminder
// at SENT; db.ReclaimStaleReminders returns such rows to PENDING after a grace
// period so a later tick retries, or gives up on one whose confirmation is being
// rejected every time and marks it FAILED.
//
// It never blocks the cron loop: the reclaim and claim are single queries and
// SendAction is fire-and-forget.
func Remind(ctx context.Context) {
	if outcome, err := db.ReclaimStaleReminders(ctx, time.Now()); err != nil {
		log.Z.Error("failed to reclaim stale reminders", zap.Error(err))
	} else {
		if outcome.Retried > 0 {
			log.Z.Warn("reclaimed stale reminders for retry", zap.Int64("count", outcome.Retried))
		}
		// Distinct from a retry and worth its own line: these reminders are now
		// terminal and will never be delivered.
		if outcome.FailedOut > 0 {
			log.Z.Error("gave up on reminders after too many delivery attempts",
				zap.Int64("count", outcome.FailedOut))
		}
	}

	claimed, err := db.ClaimDueReminders(ctx, time.Now())
	if err != nil {
		log.Z.Error("failed to claim due reminders", zap.Error(err))
		return
	}

	for _, c := range claimed {
		action := pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION
		platform := pb.Platform(c.PlatformEnum)

		resp := pb.OpenClientActionStreamResp_builder{
			PlatformEnum: &platform,
			ClientAction: &action,
			Content: reminder.NewDeliveryPayload(
				c.ID, deref(c.Message), deref(c.DestinationUID), deref(c.OwnerPlatformUID),
			),
		}.Build()

		service.ReverseServer.SendAction(resp)
	}
}

// deref reads a nullable string column, defaulting a NULL to empty.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
