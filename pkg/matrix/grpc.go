package matrix

import (
	"context"

	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// NewMatrixClient dials the gRPC server and initialises every service client.
//
// It deliberately does NOT start the reverse action stream. That happens in
// InitializeMatrix, once matrixClient exists — see startActionStream.
//
// The context parameter is therefore unused, and is retained only to keep this
// signature identical to NewDiscordClient's, which is unused for the same
// reason. cmd/ginbot-matrix still passes ctx, so dropping it here would make the
// two binaries diverge for no gain — do not "clean it up" in one without the
// other.
func NewMatrixClient(_ context.Context) {
	serverAddress := config.Options.GRPC.Host + ":" + config.Options.GRPC.Port

	conn, err := grpc.NewClient(serverAddress, config.Options.Matrix.GRPCClientOptions.DialOptions...)
	if err != nil {
		log.Z.Fatal("failed to connect to gRPC server.", zap.Error(err))
		return
	}

	client.InitUserService(conn)
	client.InitUtilityService(conn)
	client.InitReminderService(conn)
	client.InitEntertainmentService(conn)
	client.InitReverseService(conn)
	client.InitTriggerService(conn)
	client.InitRepostService(conn)
}

// startActionStream begins consuming server-pushed actions.
//
// ORDER MATTERS, and this function exists to make that order impossible to get
// wrong. Action handlers run on the stream's own goroutine, while matrixClient is
// written once by InitializeMatrix with nothing synchronising it — so starting
// the stream before that assignment is both a data race on the package variable
// and a nil dereference in any handler that reads it. Launching the goroutine
// after the assignment gives the read a happens-before edge on the write.
//
// Unlike the Discord equivalent the hazard here is prophylactic, not live: no
// entry in actionHandlers touches matrixClient today, so nothing crashes right
// now. It becomes real with the first Matrix notification handler, and the
// stream used to be started from NewMatrixClient — which cmd/ginbot-matrix calls
// BEFORE InitializeMatrix — so writing that handler would have been enough to
// break it. This seam is what makes writing one safe rather than merely lucky.
//
// It requires NewMatrixClient to have run, for ReverseServiceClient.
func startActionStream(ctx context.Context) {
	go client.RunClientActionStream(ctx, pb.Platform_PLATFORM_MATRIX_PROTOCOL, actionHandlers())
}
