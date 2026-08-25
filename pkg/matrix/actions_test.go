package matrix

import (
	"context"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestMain gives this package a logger. handleSendTest reports everything
// through log.Z, which is nil until log.InitializeLogger runs — and no test does
// — so without this the first line it logs is a nil deref that says nothing
// about the handler.
//
// matrixClient is deliberately left nil, the way pkg/discord leaves
// discordSession nil: a handler that must not reach the homeserver is proved not
// to by the fact that reaching it would panic here.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// heartbeatAction builds a SEND_TEST action carrying test, nil leaving the oneof
// arm unset.
func heartbeatAction(test *pb.TestAction) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_MATRIX_PROTOCOL.Enum(),
		ClientAction: &action,
		Test:         test,
	}.Build()
}

// TestHandleSendTestSurvivesEveryPayloadArm.
//
// This is pkg/discord's assertion repeated because the handler is a SEPARATE
// implementation, not a shared one: pkg/matrix has its own handleSendTest, so a
// nil guard added to one and not the other is exactly the drift worth catching.
//
// Matrix is also the platform with no notification handler at all
// (actionHandlers registers SEND_TEST and nothing else), which makes the
// heartbeat this client's whole exposure to the reverse stream. A panic in it
// takes down every other Matrix capability with it: client.dispatch recovers,
// but the recovery buys one lost action, not a licence to nil-deref.
func TestHandleSendTestSurvivesEveryPayloadArm(t *testing.T) {
	if matrixClient != nil {
		t.Fatal("matrixClient must stay nil for this test to mean anything")
	}

	tests := []struct {
		name string
		in   *pb.OpenClientActionStreamResp
	}{
		{
			// A server built before TestAction existed sends the action with no
			// arm at all.
			name: "no payload arm set",
			in:   heartbeatAction(nil),
		},
		{
			// The arm is set but its only field is not, so GetEmittedAt returns
			// nil and AsTime is called on that.
			name: "the heartbeat arm with no emission time",
			in:   heartbeatAction(pb.TestAction_builder{}.Build()),
		},
		{
			name: "a fully populated heartbeat",
			in:   heartbeatAction(pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build()),
		},
		{
			// An arm this handler is not for. Nothing in the schema ties an
			// action to an arm, so this is an ordinary input.
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
			// A panic fails the test by itself; no recover() is installed.
			handleSendTest(context.Background(), tt.in)
		})
	}
}

// TestMatrixRegistersNoNotificationHandler pins the deliberate gap.
//
// A reminder addressed to Matrix is pushed, found to have no handler, logged by
// client.dispatch and never confirmed — which is safe ONLY because the server's
// reclaim spends an attempt each time and fails the reminder out. If a handler
// is added here, that reasoning changes and the comment on actionHandlers has to
// change with it; if the SEND_TEST entry is dropped, the reverse stream stops
// being exercised on this platform at all.
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
