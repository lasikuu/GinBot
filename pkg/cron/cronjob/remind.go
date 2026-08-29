package cronjob

import (
	"context"
	"time"

	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// Remind reclaims lost reminders, claims the due ones and pushes each over the
// reverse stream. ClaimDueReminders' atomic PENDING -> SENT prevents doubles.
func Remind(ctx context.Context) {
	if outcome, err := db.ReclaimStaleReminders(ctx, time.Now()); err != nil {
		log.Z.Error("failed to reclaim stale reminders", zap.Error(err))
	} else {
		if outcome.Retried > 0 {
			log.Z.Warn("reclaimed stale reminders for retry", zap.Int64("count", outcome.Retried))
		}
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

		// Locals, not &c.ID: the builder keeps the pointers it is handed.
		reminderID := c.ID
		message := deref(c.Message)
		destinationUID := deref(c.DestinationUID)
		ownerUID := deref(c.OwnerPlatformUID)

		resp := pb.OpenClientActionStreamResp_builder{
			PlatformEnum: &platform,
			ClientAction: &action,
			ReminderDelivery: pb.ReminderDelivery_builder{
				ReminderId:     &reminderID,
				Message:        &message,
				DestinationUid: &destinationUID,
				OwnerUid:       &ownerUID,
			}.Build(),
		}.Build()

		service.ReverseServer.SendAction(resp)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
