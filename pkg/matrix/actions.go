package matrix

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// actionHandlers maps server-pushed actions to their Matrix implementations.
//
// CLIENT_ACTION_SEND_NOTIFICATION is deliberately absent: there is no Matrix
// notification implementation yet, and writing one is out of scope here. A
// reminder whose destination is PLATFORM_MATRIX_PROTOCOL is therefore pushed,
// logged by client.dispatch as "no handler registered", and never confirmed.
//
// That does not loop forever. The server's reclaim counts delivery attempts and
// marks such a reminder FAILED once maxDeliveryAttempts is spent
// (pkg/db/reminder.go), so an unhandled notification fails out instead of being
// re-pushed every grace period indefinitely.
//
// It is latent today — no Matrix command surface can create a reminder — but it
// becomes real the moment one exists, so the bound is what makes leaving this
// unimplemented safe rather than merely untested.
func actionHandlers() client.ActionHandlers {
	return client.ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: handleSendTest,
	}
}

// handleSendTest logs a development-only heartbeat pushed by cron.
//
// The emission time is logged rather than the whole message: the action itself
// says nothing an operator does not already know, so the only information in a
// heartbeat is how stale it is by the time it lands here.
func handleSendTest(_ context.Context, in *pb.OpenClientActionStreamResp) {
	if !in.HasTest() {
		// Not a defect on either end: SEND_TEST carries no payload from a
		// server built before TestAction existed, and the heartbeat is still a
		// heartbeat without one.
		log.Z.Debug("received test action with no test payload")
		return
	}

	log.Z.Debug("received test action",
		zap.Time("emitted_at", in.GetTest().GetEmittedAt().AsTime()))
}
