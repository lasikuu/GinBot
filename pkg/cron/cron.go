package cron

import (
	"context"
	"time"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/cron/cronjob"
	"github.com/lasikuu/GinBot/pkg/enum"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// jobSet is the work RunCronJobs dispatches; a nil field is skipped.
type jobSet struct {
	Remind               func(ctx context.Context)
	SendTestAction       func()
	CongratulateBirthday func()
	CollectOrphanFiles   func(ctx context.Context)
	SweepRepostEntries   func(ctx context.Context)
	PruneForcedLimiter   func()
}

// defaultJobs keeps every gate inside its closure: the singletons it reads
// must be read at tick time, not at construction.
func defaultJobs() jobSet {
	return jobSet{
		Remind: cronjob.Remind,
		SendTestAction: func() {
			if config.AppEnvironment != enum.DEVELOPMENT {
				return
			}

			platformEnum := pb.Platform_PLATFORM_DISCORD
			clientAction := pb.ClientAction_CLIENT_ACTION_SEND_TEST

			resp := pb.OpenClientActionStreamResp_builder{
				PlatformEnum: &platformEnum,
				ClientAction: &clientAction,
				// Stamped at emission so the client can log the push latency.
				Test: pb.TestAction_builder{
					EmittedAt: timestamppb.Now(),
				}.Build(),
			}.Build()
			service.ReverseServer.SendAction(resp)
		},
		CongratulateBirthday: cronjob.CongratulateBirthday,
		CollectOrphanFiles:   cronjob.CollectOrphanFiles,
		SweepRepostEntries:   cronjob.SweepRepostEntries,
		PruneForcedLimiter: func() {
			// The cron loop may start before the services are wired up.
			if service.TriggerServer != nil {
				service.TriggerServer.PruneForcedLimiter()
			}
		},
	}
}

func RunCronJobs(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	runCronJobs(ctx, ticker.C, defaultJobs())
}

// runCronJobs pins each job to a real minute or hour boundary via wall-clock
// predicates on a 1s tick. Jobs run inline, so a slow one delays the loop.
func runCronJobs(ctx context.Context, tick <-chan time.Time, jobs jobSet) {
	for {
		var now time.Time
		select {
		case <-ctx.Done():
			log.Z.Info("cron loop stopping")
			return
		case now = <-tick:
		}

		if jobs.Remind != nil {
			jobs.Remind(ctx)
		}

		// Every minute
		if now.Second() == 0 {
			log.Z.Debug("cron running", zap.Time("time", now))

			if jobs.SendTestAction != nil {
				jobs.SendTestAction()
			}

			if jobs.CongratulateBirthday != nil {
				jobs.CongratulateBirthday()
			}
		}

		// Every hour
		if now.Minute() == 0 && now.Second() == 0 {
			log.Z.Debug("hourly cron tick", zap.Time("time", now))

			// Hourly: a table scan plus filesystem work, capped per sweep.
			if jobs.CollectOrphanFiles != nil {
				jobs.CollectOrphanFiles(ctx)
			}

			if jobs.SweepRepostEntries != nil {
				jobs.SweepRepostEntries(ctx)
			}

			// Nothing else prunes: Allow deliberately does not.
			if jobs.PruneForcedLimiter != nil {
				jobs.PruneForcedLimiter()
			}
		}
	}
}
