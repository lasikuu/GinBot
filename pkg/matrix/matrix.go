package matrix

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"google.golang.org/protobuf/types/known/emptypb"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

var matrixClient *mautrix.Client

func InitializeMatrix() {
	var err error
	if matrixClient, err = mautrix.NewClient(config.Options.Matrix.HomeServerURL, id.UserID(config.Options.Matrix.UserID), config.Options.Matrix.AccessToken); err != nil {
		log.Z.Fatal("cannot create a new session.", zap.Error(err))
	}

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
			// carries no gRPC metadata, so any RPC requiring it would fail.
			rpcCtx := callermeta.NewOutgoingContext(ctx, pb.Platform_PLATFORM_MATRIX_PROTOCOL, evt.Sender.String())

			resp, err := client.UtilityServiceClient.HealthCheck(rpcCtx, &emptypb.Empty{})
			if err != nil {
				log.Z.Error("failed to call HealthCheck", zap.Error(err))
				return
			}

			if _, err := matrixClient.SendText(ctx, evt.RoomID, resp.GetStatus().String()); err != nil {
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
