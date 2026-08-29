package matrix

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// healthCheckTimeout bounds the call below: the inherited mautrix sync context
// has no deadline, so an unresponsive server would pin the dispatch goroutine.
const healthCheckTimeout = 10 * time.Second

// matrixClient is written once here and then read from the sync dispatch and the
// action stream goroutines with nothing synchronising it, so it must be assigned
// before either is started.
var matrixClient *mautrix.Client

// InitializeMatrix blocks until the process is signalled to stop. ctx bounds the
// reverse action stream, started here because its handlers may read matrixClient.
func InitializeMatrix(ctx context.Context, clients *client.Clients) {
	var err error
	if matrixClient, err = mautrix.NewClient(config.Options.Matrix.HomeServerURL, id.UserID(config.Options.Matrix.UserID), config.Options.Matrix.AccessToken); err != nil {
		log.Z.Fatal("cannot create a new session.", zap.Error(err))
	}

	// Only once matrixClient is assigned; see the note on the variable.
	startActionStream(ctx, clients)

	selfID := id.UserID(config.Options.Matrix.UserID)

	syncer := matrixClient.Syncer.(mautrix.ExtensibleSyncer)
	syncer.OnSync(matrixClient.DontProcessOldEvents)
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		// Without this the bot reacts to its own messages.
		if evt.Sender == selfID {
			return
		}

		log.Z.Debug("message received",
			zap.String("room", evt.RoomID.String()),
			zap.String("sender", evt.Sender.String()),
		)

		if evt.Content.AsMessage().Body == "!healthcheck" {
			// The sync context carries no caller identity, so any guarded RPC
			// would be refused without this.
			rpcCtx := callermeta.NewOutgoingContext(ctx, pb.Platform_PLATFORM_MATRIX_PROTOCOL, evt.Sender.String())
			rpcCtx, cancel := context.WithTimeout(rpcCtx, healthCheckTimeout)
			defer cancel()

			resp, err := clients.Utility.HealthCheck(rpcCtx, connect.NewRequest(pb.HealthCheckReq_builder{}.Build()))
			if err != nil {
				log.Z.Error("failed to call HealthCheck", zap.Error(err))
				return
			}

			if _, err := matrixClient.SendText(ctx, evt.RoomID, resp.Msg.GetStatus().String()); err != nil {
				log.Z.Error("failed to send event", zap.Error(err))
				return
			}
		}
	})

	// TODO: setup crypto

	syncCtx, cancelSync := context.WithCancel(context.Background())
	var syncStopWait sync.WaitGroup

	syncStopWait.Go(func() {
		// Scoped to the goroutine: assigning to the outer err would race with main.
		if syncErr := matrixClient.SyncWithContext(syncCtx); syncErr != nil && !errors.Is(syncErr, context.Canceled) {
			log.Z.Error("failed to sync", zap.Error(syncErr))
		}
	})

	stop := make(chan os.Signal, 1)
	// SIGTERM as well as SIGINT, so container shutdowns are graceful.
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	cancelSync()
	syncStopWait.Wait()

	log.Z.Info("gracefully shutting down.")
}
