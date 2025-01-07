package client

import (
	"context"
	"io"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

var ReverseServiceClient pb.ReverseServiceClient

func InitReverseService(conn *grpc.ClientConn) {
	ReverseServiceClient = pb.NewReverseServiceClient(conn)
}

// RunClientActionStream opens a stream to receive client actions from the server
func RunClientActionStream(platform pb.Platform) {
	stream, err := ReverseServiceClient.OpenClientActionStream(context.Background())
	if err != nil {
		log.Z.Error("failed to open client action stream", zap.Error(err))
		return
	}

	go func() {
		for {
			in, err := stream.Recv()
			if err == io.EOF {
				// read done
				return
			}

			if err != nil {
				log.Z.Error("failed to receive action from server", zap.Error(err))
				return
			}

			log.Z.Debug("received server request",
				zap.String("client_action", in.GetClientAction().String()),
				zap.String("platform", in.GetPlatformEnum().String()),
				zap.Any("data", in.GetContent()),
			)
		}
	}()

	req := pb.OpenClientActionStreamReq_builder{
		PlatformEnum: platform.Enum(),
	}.Build()
	if err := stream.Send(req); err != nil {
		log.Z.Error("failed to send action to server", zap.Error(err))
		return
	}
}
