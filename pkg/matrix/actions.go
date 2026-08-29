package matrix

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// TODO: CLIENT_ACTION_SEND_NOTIFICATION has no Matrix implementation, so such a
// reminder is never confirmed and fails out at maxDeliveryAttempts.
func actionHandlers() client.ActionHandlers {
	return client.ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: handleSendTest,
	}
}

// handleSendTest logs a development-only heartbeat pushed by cron.
func handleSendTest(_ context.Context, in *pb.OpenClientActionStreamResp) {
	if !in.HasTest() {
		// Not a defect: a server built before TestAction sends no payload.
		log.Z.Debug("received test action with no test payload")
		return
	}

	log.Z.Debug("received test action",
		zap.Time("emitted_at", in.GetTest().GetEmittedAt().AsTime()))
}
