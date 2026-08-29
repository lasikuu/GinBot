package server

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// registrationVerdictGrace bounds the wait for a refusal: there is no positive
// admission signal on the wire, so acceptance is only ever inferred from its absence.
const registrationVerdictGrace = 250 * time.Millisecond

// openWithHeader sets ginbot-platform-enum by hand; identityInterceptor can only ever
// stamp a real pb.Platform. Cancelled on cleanup so no admitted stream parks a handler.
func openWithHeader(t *testing.T, h *harness, value string) *connect.BidiStreamForClient[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp] {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream := h.Reverse.OpenClientActionStream(ctx)
	if value != "" {
		stream.RequestHeader().Set(callermeta.HeaderPlatformEnum, value)
	}
	return stream
}

// registrationVerdict returns nil when nothing was rejected within the grace period.
// The error comes from Receive: a streaming Send is asynchronous and never carries it.
func registrationVerdict(t *testing.T, stream *connect.BidiStreamForClient[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp]) error {
	t.Helper()

	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	// Buffered so this goroutine is not left parked once the grace period expires.
	ended := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Receive(); err != nil {
				ended <- err
				return
			}
		}
	}()

	select {
	case err := <-ended:
		return err
	case <-time.After(registrationVerdictGrace):
		return nil
	}
}

// Registered under PLATFORM_UNSPECIFIED, or the refusal would be FailedPrecondition.
func TestAnUnspecifiedPlatformHeaderIsRejected(t *testing.T) {
	user := testUser(reverseCallerUserID, pb.Clearance_CLEARANCE_REGISTERED)
	dir := newDirectory().add(pb.Platform_PLATFORM_UNSPECIFIED, reverseCallerUID, user)
	h := newHarness(t, withDirectory(dir))

	stream := openWithHeader(t, h, pb.Platform_PLATFORM_UNSPECIFIED.String())

	err := registrationVerdict(t, stream)
	if err == nil {
		t.Fatal("the server admitted a stream whose ginbot-platform-enum header was PLATFORM_UNSPECIFIED")
	}
	requireCode(t, err, connect.CodeInvalidArgument)

	if n := dir.resolveCount(); n != 0 {
		t.Errorf("resolver ran %d times for an unspecified platform, want 0: "+
			"callermeta.FromHeader is supposed to refuse it before any caller is resolved", n)
	}
}

// The header carries a NAME, so "undefined" is a string no Platform declares.
func TestAnUnrecognisedPlatformHeaderIsRejected(t *testing.T) {
	h := reverseHarness(t)

	stream := openWithHeader(t, h, "PLATFORM_DOES_NOT_EXIST")

	err := registrationVerdict(t, stream)
	if err == nil {
		t.Fatal("the server admitted a stream whose ginbot-platform-enum header names no Platform value")
	}
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestAMissingPlatformHeaderIsRejected(t *testing.T) {
	h := reverseHarness(t)

	stream := openWithHeader(t, h, "")

	err := registrationVerdict(t, stream)
	if err == nil {
		t.Fatal("the server admitted a stream with no ginbot-platform-enum header at all")
	}
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestAValidPlatformHeaderStillOpensTheStream(t *testing.T) {
	for _, platform := range []pb.Platform{
		pb.Platform_PLATFORM_DISCORD,
		pb.Platform_PLATFORM_MATRIX_PROTOCOL,
	} {
		t.Run(platform.String(), func(t *testing.T) {
			h := reverseHarness(t)

			stream := openWithHeader(t, h, platform.String())
			// A valid platform is not enough: the caller must resolve, which needs ginbot-user-id.
			stream.RequestHeader().Set(callermeta.HeaderUserID, reverseCallerUID)

			if err := registrationVerdict(t, stream); err != nil {
				t.Errorf("the server refused a %v registration with %v", platform, err)
			}
		})
	}
}
