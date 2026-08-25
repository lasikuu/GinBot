package discord

import (
	"context"

	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// NewDiscordClient dials the gRPC server and initialises every service client.
//
// It deliberately does NOT start the reverse action stream. That happens in
// InitializeDiscord, once the Discord session exists — see startActionStream.
func NewDiscordClient(_ context.Context) {
	serverAddress := config.Options.GRPC.Host + ":" + config.Options.GRPC.Port

	conn, err := grpc.NewClient(serverAddress, config.Options.Discord.GRPCClientOptions.DialOptions...)
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
// wrong. Action handlers run on the stream's own goroutine and a notification
// handler uses discordSession, so the stream must not start until that variable
// has been assigned: starting it first is both a data race on the package
// variable and a nil dereference in the handler that reads it. Launching the
// goroutine after the assignment gives the read a happens-before edge on the
// write.
//
// The hazard here is live, not prophylactic — unlike the Matrix equivalent,
// handleSendNotification reads discordSession today, so getting the order wrong
// breaks the first reminder that arrives rather than some future one. What it
// costs is now bounded: pkg/grpc/client.dispatch recovers around the inline
// handler call, so the deref loses that one delivery instead of killing the
// process. Bounded is not acceptable, which is why the seam stays.
//
// It requires NewDiscordClient to have run, for ReverseServiceClient.
func startActionStream(ctx context.Context) {
	go client.RunClientActionStream(ctx, pb.Platform_PLATFORM_DISCORD, actionHandlers())
}
