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

func NewMatrixClient(ctx context.Context) {
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

	go client.RunClientActionStream(ctx, pb.Platform_PLATFORM_MATRIX_PROTOCOL, actionHandlers())
}
