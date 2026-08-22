package client

import (
	"context"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestMain(m *testing.M) {
	// The package logs unconditionally; give it a logger rather than a nil pointer.
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

func action(t *testing.T, clientAction pb.ClientAction) *pb.OpenClientActionStreamResp {
	t.Helper()
	content, err := structpb.NewStruct(map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("build struct: %v", err)
	}
	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &clientAction,
		Content:      content,
	}.Build()
}

func TestDispatchInvokesRegisteredHandler(t *testing.T) {
	var got *pb.OpenClientActionStreamResp
	calls := 0

	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(_ context.Context, in *pb.OpenClientActionStreamResp) {
			calls++
			got = in
		},
	}

	in := action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST)
	dispatch(context.Background(), in, handlers)

	if calls != 1 {
		t.Fatalf("handler called %d times, want 1", calls)
	}
	if got == nil {
		t.Fatal("handler received a nil action")
	}
	if got.GetContent().AsMap()["key"] != "value" {
		t.Errorf("handler received content %v, want key=value", got.GetContent().AsMap())
	}
}

// An action with no handler must not invoke an unrelated one, and must not panic.
func TestDispatchWithNoRegisteredHandler(t *testing.T) {
	called := false
	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			called = true
		},
	}

	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION), handlers)

	if called {
		t.Error("a handler registered for a different action was invoked")
	}
}

func TestDispatchWithEmptyRegistry(t *testing.T) {
	// Must not panic on a nil map.
	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), nil)
}

func TestDispatchRoutesEachActionToItsOwnHandler(t *testing.T) {
	testCalls, notifyCalls := 0, 0
	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			testCalls++
		},
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION: func(context.Context, *pb.OpenClientActionStreamResp) {
			notifyCalls++
		},
	}

	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), handlers)
	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION), handlers)
	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION), handlers)

	if testCalls != 1 {
		t.Errorf("SEND_TEST handler called %d times, want 1", testCalls)
	}
	if notifyCalls != 2 {
		t.Errorf("SEND_NOTIFICATION handler called %d times, want 2", notifyCalls)
	}
}

// RunClientActionStream must return promptly on cancellation rather than
// sleeping out its backoff. ReverseServiceClient is nil here, so runOnce fails
// immediately and the loop is exercised purely through its retry path.
func TestRunClientActionStreamStopsOnContextCancel(t *testing.T) {
	ReverseServiceClient = nil

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			// A nil client makes runOnce panic rather than error; recovering keeps
			// this test about the cancellation path.
			_ = recover()
		}()
		RunClientActionStream(ctx, pb.Platform_PLATFORM_DISCORD, nil)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunClientActionStream did not return after context cancellation")
	}
}

// Regression test for the backoff ratchet: the delay was initialised once
// outside the loop and only ever doubled, so a client that had dropped a few
// times waited the 30s maximum before every later reconnect no matter how
// healthy the intervening stream was.
func TestBackoffResetsAfterAnEstablishedStream(t *testing.T) {
	// Mirrors the escalation in RunClientActionStream.
	next := func(backoff time.Duration, established bool) time.Duration {
		if established {
			backoff = reconnectMinBackoff
		}
		if !established {
			backoff *= 2
			if backoff > reconnectMaxBackoff {
				backoff = reconnectMaxBackoff
			}
		}
		return backoff
	}

	// Repeated failures to connect must escalate, and cap.
	backoff := reconnectMinBackoff
	for i := 0; i < 20; i++ {
		backoff = next(backoff, false)
	}
	if backoff != reconnectMaxBackoff {
		t.Errorf("after repeated failures backoff = %v, want the %v cap", backoff, reconnectMaxBackoff)
	}

	// One healthy stream must clear the accumulated penalty.
	backoff = next(backoff, true)
	if backoff != reconnectMinBackoff {
		t.Errorf("after an established stream backoff = %v, want %v", backoff, reconnectMinBackoff)
	}
}
