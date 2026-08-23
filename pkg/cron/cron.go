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

// jobSet is the work RunCronJobs dispatches. Every field is optional: a nil
// field is skipped, so the schedule can be exercised independently of the jobs.
//
// Unexported on purpose. It exists so runCronJobs can be driven without the
// production singletons behind cronjob and service, not as a way for a caller
// to supply its own schedule — cmd/ginbot-server calls RunCronJobs and nothing
// else. Export it if and when something outside this package needs it.
type jobSet struct {
	Remind               func(ctx context.Context)
	SendTestAction       func()
	CongratulateBirthday func()
	CollectOrphanFiles   func(ctx context.Context)
	SweepRepostEntries   func(ctx context.Context)
	PruneForcedLimiter   func()
}

// defaultJobs is the production wiring.
//
// Every gate and lookup that used to sit in the loop body stays INSIDE its
// closure rather than being resolved here. That matters: config.AppEnvironment
// and service.TriggerServer are package-level singletons whose values at
// construction time are not necessarily their values at tick time, and this
// deliberately preserves that late binding.
func defaultJobs() jobSet {
	return jobSet{
		Remind: cronjob.Remind,
		SendTestAction: func() {
			if config.AppEnvironment != enum.DEVELOPMENT {
				return
			}

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
		},
		CongratulateBirthday: cronjob.CongratulateBirthday,
		CollectOrphanFiles:   cronjob.CollectOrphanFiles,
		SweepRepostEntries:   cronjob.SweepRepostEntries,
		PruneForcedLimiter: func() {
			// Nil-checked at tick time, not at construction: the cron loop can
			// legitimately start before the gRPC services are wired up.
			if service.TriggerServer != nil {
				service.TriggerServer.PruneForcedLimiter()
			}
		},
	}
}

// RunCronJobs runs the periodic job loop until ctx is cancelled.
func RunCronJobs(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	runCronJobs(ctx, ticker.C, defaultJobs())
}

// runCronJobs is the schedule itself, taking its clock and its work as
// parameters so both can be substituted.
//
// It is a 1-second tick with wall-clock predicates rather than a tree of
// per-interval tickers: the minute and hour boundaries are what the schedule
// is actually expressed in, and comparing now.Second()/now.Minute() against 0
// keeps every job pinned to the real boundary instead of drifting by whenever
// the process happened to start.
//
// Jobs run INLINE on this goroutine, so a slow one delays the whole loop.
// That is intentional — it bounds concurrency to one and means a job can never
// overlap itself — and each job is individually responsible for staying short
// (see the orphan sweep's batch limit).
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

			// Hourly rather than per minute: it is a table scan plus filesystem
			// work, and an abandoned blob costs only disk until it is collected.
			// It runs INLINE and so does delay this loop, but boundedly — one
			// sweep is capped at orphanBatchLimit rows, and a large backlog
			// drains over several hours rather than in one long tick.
			if jobs.CollectOrphanFiles != nil {
				jobs.CollectOrphanFiles(ctx)
			}

			// Same reasoning as the orphan sweep above: retention defaults to
			// forever (W1), so most instances are never even listed, but the
			// ones with a finite window still need a periodic pass rather
			// than an unbounded delete on read.
			if jobs.SweepRepostEntries != nil {
				jobs.SweepRepostEntries(ctx)
			}

			// Cheap, and nothing else does it: ForcedLimiter documents that
			// Allow deliberately does not prune, so its map grows by one entry
			// per author who mentions the bot until someone calls this.
			if jobs.PruneForcedLimiter != nil {
				jobs.PruneForcedLimiter()
			}
		}
	}
}
