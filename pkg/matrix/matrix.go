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

// healthCheckTimeout bounds the outgoing HealthCheck call below. The sync
// context it would otherwise inherit carries no deadline of its own — it is
// mautrix's own sync loop context, not a request-scoped one — so without this
// an unresponsive server would hold the sync dispatch goroutine open
// indefinitely.
const healthCheckTimeout = 10 * time.Second

// matrixClient is written once by InitializeMatrix and then read from several
// goroutines (mautrix's sync dispatch, and the reverse action stream). Nothing
// synchronises it, so it must be assigned BEFORE any of those readers is
// started — see startActionStream.
var matrixClient *mautrix.Client

// InitializeMatrix brings up the Matrix client and blocks until the process is
// signalled to stop.
//
// ctx bounds the reverse action stream, which is started from here rather than
// alongside the Connect clients precisely because its handlers may read
// matrixClient. clients are the service clients dialed by NewMatrixClient.
func InitializeMatrix(ctx context.Context, clients *client.Clients) {
	var err error
	if matrixClient, err = mautrix.NewClient(config.Options.Matrix.HomeServerURL, id.UserID(config.Options.Matrix.UserID), config.Options.Matrix.AccessToken); err != nil {
		log.Z.Fatal("cannot create a new session.", zap.Error(err))
	}

	// Only now: matrixClient is assigned, so an action arriving on the first tick
	// has a client to post through.
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
			// Caller identity has to be attached explicitly; the sync context
			// carries no identity of its own, so any RPC requiring it would fail.
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
	syncStopWait.Add(1)

	go func() {
		defer syncStopWait.Done()
		// Scoped to the goroutine: assigning to the outer err would race with main.
		if syncErr := matrixClient.SyncWithContext(syncCtx); syncErr != nil && !errors.Is(syncErr, context.Canceled) {
			log.Z.Error("failed to sync", zap.Error(syncErr))
		}
	}()

	stop := make(chan os.Signal, 1)
	// SIGTERM as well as SIGINT, so container shutdowns are graceful.
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	cancelSync()
	syncStopWait.Wait()

	log.Z.Info("gracefully shutting down.")
}
