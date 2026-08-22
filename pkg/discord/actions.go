package discord

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// actionHandlers maps server-pushed actions to their Discord implementations.
func actionHandlers() client.ActionHandlers {
	return client.ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: handleSendTest,
	}
}

// handleSendTest logs a development-only heartbeat pushed by cron.
func handleSendTest(_ context.Context, in *pb.OpenClientActionStreamResp) {
	log.Z.Debug("received test action", zap.Any("content", in.GetContent().AsMap()))
}
