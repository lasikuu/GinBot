package matrix

import (
	"context"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestMain gives this package a logger: log.Z is nil until it is initialised.
// matrixClient is left nil, so a handler that reached the homeserver would panic.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// heartbeatAction leaves the oneof arm unset when test is nil.
func heartbeatAction(test *pb.TestAction) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_MATRIX_PROTOCOL.Enum(),
		ClientAction: &action,
		Test:         test,
	}.Build()
}

func TestHandleSendTestSurvivesEveryPayloadArm(t *testing.T) {
	if matrixClient != nil {
		t.Fatal("matrixClient must stay nil for this test to mean anything")
	}

	tests := []struct {
		name string
		in   *pb.OpenClientActionStreamResp
	}{
		{
			name: "no payload arm set",
			in:   heartbeatAction(nil),
		},
		{
			// GetEmittedAt returns nil here, and AsTime is called on that.
			name: "the heartbeat arm with no emission time",
			in:   heartbeatAction(pb.TestAction_builder{}.Build()),
		},
		{
			name: "a fully populated heartbeat",
			in:   heartbeatAction(pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build()),
		},
		{
			name: "a reminder delivery on a heartbeat action",
			in: func() *pb.OpenClientActionStreamResp {
				action := pb.ClientAction_CLIENT_ACTION_SEND_TEST
				reminderID, message := "id-1", "stretch"

				return pb.OpenClientActionStreamResp_builder{
					PlatformEnum: pb.Platform_PLATFORM_MATRIX_PROTOCOL.Enum(),
					ClientAction: &action,
					ReminderDelivery: pb.ReminderDelivery_builder{
						ReminderId: &reminderID,
						Message:    &message,
					}.Build(),
				}.Build()
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handleSendTest(context.Background(), tt.in)
		})
	}
}

func TestMatrixRegistersNoNotificationHandler(t *testing.T) {
	handlers := actionHandlers()

	if _, ok := handlers[pb.ClientAction_CLIENT_ACTION_SEND_TEST]; !ok {
		t.Error("no SEND_TEST handler registered; nothing exercises the Matrix reverse stream")
	}
	for _, action := range []pb.ClientAction{
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION,
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION_URGENT,
	} {
		if _, ok := handlers[action]; ok {
			t.Errorf("a %v handler appeared; see actionHandlers' comment on why its absence is load-bearing",
				action)
		}
	}
}
