package cron

import (
	"context"
	"time"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/cron/cronjob"
	"github.com/lasikuu/GinBot/pkg/enum"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// RunCronJobs runs the periodic job loop until ctx is cancelled.
func RunCronJobs(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		var now time.Time
		select {
		case <-ctx.Done():
			log.Z.Info("cron loop stopping")
			return
		case now = <-ticker.C:
		}

		cronjob.Remind(ctx)

		// Every minute
		if now.Second() == 0 {
			log.Z.Debug("cron running", zap.Time("time", now))

			if config.AppEnvironment == enum.DEVELOPMENT {
				platformEnum := pb.Platform_PLATFORM_DISCORD
				clientAction := pb.ClientAction_CLIENT_ACTION_SEND_TEST
				content := structpb.Struct{
					Fields: map[string]*structpb.Value{
						"test": structpb.NewStringValue("test"),
					},
				}

				resp := pb.OpenClientActionStreamResp_builder{
					PlatformEnum: &platformEnum,
					ClientAction: &clientAction,
					Content:      &content,
				}.Build()
				service.ReverseServer.SendAction(resp)
			}

			cronjob.CongratulateBirthday()
		}

		// Every hour
		if now.Minute() == 0 && now.Second() == 0 {
			log.Z.Debug("hourly cron tick", zap.Time("time", now))

			// Hourly rather than per minute: it is a table scan plus filesystem
			// work, and an abandoned blob costs only disk until it is collected.
			// It runs INLINE and so does delay this loop, but boundedly — one
			// sweep is capped at orphanBatchLimit rows, and a large backlog
			// drains over several hours rather than in one long tick.
			cronjob.CollectOrphanFiles(ctx)

			// Same reasoning as the orphan sweep above: retention defaults to
			// forever (W1), so most instances are never even listed, but the
			// ones with a finite window still need a periodic pass rather
			// than an unbounded delete on read.
			cronjob.SweepRepostEntries(ctx)

			// Cheap, and nothing else does it: ForcedLimiter documents that
			// Allow deliberately does not prune, so its map grows by one entry
			// per author who mentions the bot until someone calls this.
			if service.TriggerServer != nil {
				service.TriggerServer.PruneForcedLimiter()
			}
		}
	}
}
