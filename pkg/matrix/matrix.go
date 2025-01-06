package matrix

import (
	"context"
	"errors"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"google.golang.org/protobuf/types/known/emptypb"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"os"
	"os/signal"
	"sync"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

var matrixClient *mautrix.Client

func InitializeMatrix() {
	var err error
	if matrixClient, err = mautrix.NewClient(config.Options.Matrix.HomeServerURL, id.UserID(config.Options.Matrix.UserID), config.Options.Matrix.AccessToken); err != nil {
		log.Z.Fatal("cannot create a new session.", zap.Error(err))
	}

	var lastRoomID id.RoomID

	syncer := matrixClient.Syncer.(mautrix.ExtensibleSyncer)
	syncer.OnSync(matrixClient.DontProcessOldEvents)
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		log.Z.Info("message received")

		lastRoomID = evt.RoomID
		if evt.Content.AsMessage().Body == "!healthcheck" {
			resp, err := client.UtilityServiceClient.HealthCheck(ctx, &emptypb.Empty{})
			if err != nil {
				log.Z.Error("failed to call HealthCheck", zap.Error(err))
				return
			}

			_, err = matrixClient.SendText(ctx, lastRoomID, resp.GetStatus().String())
			if err != nil {
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
		err = matrixClient.SyncWithContext(syncCtx)
		defer syncStopWait.Done()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Z.Error("failed to sync", zap.Error(err))
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop

	cancelSync()
	syncStopWait.Wait()

	log.Z.Info("gracefully shutting down.")
}
