package server

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"google.golang.org/grpc/codes"
)

// This file used to pin protovalidate rejecting OpenClientActionStreamReq's
// platform_enum field — three rules (required, enum.defined_only,
// enum.not_in = 0), exercised through connectvalidate.NewInterceptor. That
// field, and every field on the message, is gone: OpenClientActionStreamReq
// carries nothing at all now (see reverse.proto's own doc comment on it),
// identity having moved to the ginbot-platform-enum header, so
// connectvalidate has nothing left to validate on this RPC at all.
//
// What replaces this coverage is the same refusal through the new mechanism:
// a stream opened with a PLATFORM_UNSPECIFIED ginbot-platform-enum header
// must still be refused InvalidArgument, and one carrying a value no
// Platform name matches must be refused the same way `enum.defined_only`
// used to refuse an undefined NUMBER on the message. Both now happen at
// callermeta.FromHeader, reached from ClearanceInterceptor.WrapStreamingHandler
// (pkg/grpc/interceptor/clearance.go) rather than from protovalidate — see
// that function's own three cases (empty, unrecognised, PLATFORM_UNSPECIFIED)
// for exactly what is being exercised here from the other side of the wire.
//
// Everything here goes through reverseHarness, a real HTTP/2 connection with
// the production interceptor chain in front of it — the only place that
// chain actually runs. reverse_test.go's registry and fan-out tests exercise
// ReverseServer's own methods directly and never reach the chain at all; the
// two files therefore cover two different lines of defence, and neither
// substitutes for the other.

// registrationVerdictGrace bounds how long a test waits for the stream to be
// refused before concluding it was accepted.
//
// There is no positive admission signal on the wire: a stream the server
// admits simply blocks in Receive, so "accepted" can only be inferred from
// the absence of a rejection, and inferring it takes a wait. 250ms against a
// measured sub-millisecond round trip over a real loopback HTTP/2 connection
// under -race. Too short would not produce a false PASS on the rejection
// tests — those block on Receive until the status arrives — only a false
// pass on an acceptance test, which is why the margin is generous rather
// than tight.
const registrationVerdictGrace = 250 * time.Millisecond

// openWithHeader opens a reverse stream whose ginbot-platform-enum header is
// set directly, bypassing callerCtx/identityInterceptor entirely — that path
// only ever stamps a real pb.Platform's own String(), which can never
// produce an unrecognised value, and callerCtx's platform argument is typed
// as pb.Platform so it cannot express PLATFORM_UNSPECIFIED's absence either.
// A garbage string is exactly what this file needs to drive
// callermeta.FromHeader's "unrecognised" branch, so the header is written by
// hand onto the stream's own RequestHeader before the first Send.
//
// The context is cancelled on cleanup rather than left as Background: an
// ADMITTED stream (TestAValidPlatformHeaderStillOpensTheStream) otherwise
// parks its handler in the select loop for the rest of the test binary,
// which is exactly what leaves an idle-but-open HTTP/2 connection for
// httptest.Server.Close to hang on — see harness_test.go's own comment on
// httpClient.CloseIdleConnections for the full explanation.
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

// registrationVerdict opens a client action stream carrying header, sends one
// hello, and reports how the server answered.
//
// The error is read from Receive rather than from Send. A streaming Send is
// asynchronous — it hands the message to the transport and returns — so a
// rejection produced by the interceptor arrives as the stream's terminal
// status on the receive side, never as the send's return value. Asserting on
// Send would pass whatever the interceptor did.
//
// nil means the server did not reject the stream within the grace period.
func registrationVerdict(t *testing.T, stream *connect.BidiStreamForClient[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp]) error {
	t.Helper()

	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		// A send that fails outright is not the rejection under test: the
		// interceptor rejects the request after the transport carried it, and
		// reporting that here would look identical to the test passing.
		t.Fatalf("send hello: %v", err)
	}

	// Buffered so this goroutine cannot be left parked on a receive once the
	// grace period has expired and the test has moved on.
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

// TestAnUnspecifiedPlatformHeaderIsRejected is what callermeta.FromHeader's
// own PLATFORM_UNSPECIFIED check now buys on this RPC, in place of the old
// enum.not_in = 0 rule on the message.
//
// The identity is registered anyway, under PLATFORM_UNSPECIFIED specifically:
// if this caller were only ever known under a real platform, opening with an
// unspecified header would be refused with FailedPrecondition ("caller is not
// registered") instead, for an unrelated reason, and this test would pass for
// the wrong one. dir.resolveCount() below is what proves that did not happen
// — FromHeader fails before ClearanceInterceptor ever calls the resolver.
func TestAnUnspecifiedPlatformHeaderIsRejected(t *testing.T) {
	user := testUser(reverseCallerUserID, pb.Clearance_CLEARANCE_REGISTERED)
	dir := newDirectory().add(pb.Platform_PLATFORM_UNSPECIFIED, reverseCallerUID, user)
	h := newHarness(t, withDirectory(dir))

	stream := openWithHeader(t, h, pb.Platform_PLATFORM_UNSPECIFIED.String())

	err := registrationVerdict(t, stream)
	if err == nil {
		t.Fatal("the server admitted a stream whose ginbot-platform-enum header was PLATFORM_UNSPECIFIED")
	}
	requireCode(t, err, codes.InvalidArgument)

	if n := dir.resolveCount(); n != 0 {
		t.Errorf("resolver ran %d times for an unspecified platform, want 0: "+
			"callermeta.FromHeader is supposed to refuse it before any caller is resolved", n)
	}
}

// TestAnUnrecognisedPlatformHeaderIsRejected is what callermeta.FromHeader's
// pb.Platform_value lookup buys, in place of the old enum.defined_only rule
// on a message field. The header carries a NAME now, not a raw number, so
// "undefined" means a string no Platform value declares rather than a number
// outside the enum's range — the mechanism moved, but a client that gets the
// platform wrong is refused the same way either side of that move.
func TestAnUnrecognisedPlatformHeaderIsRejected(t *testing.T) {
	h := reverseHarness(t)

	stream := openWithHeader(t, h, "PLATFORM_DOES_NOT_EXIST")

	err := registrationVerdict(t, stream)
	if err == nil {
		t.Fatal("the server admitted a stream whose ginbot-platform-enum header names no Platform value")
	}
	requireCode(t, err, codes.InvalidArgument)
}

// TestAMissingPlatformHeaderIsRejected is what callermeta.FromHeader's
// presence check buys, in place of the old `required` rule on the message.
// No header at all is a real input: a client built without
// identityInterceptor's equivalent (or a raw curl against the endpoint)
// sends exactly this.
func TestAMissingPlatformHeaderIsRejected(t *testing.T) {
	h := reverseHarness(t)

	stream := openWithHeader(t, h, "")

	err := registrationVerdict(t, stream)
	if err == nil {
		t.Fatal("the server admitted a stream with no ginbot-platform-enum header at all")
	}
	requireCode(t, err, codes.InvalidArgument)
}

// TestAValidPlatformHeaderStillOpensTheStream is the other half of every
// rejection above: a mechanism that refused everything would satisfy all
// three rejection tests too.
func TestAValidPlatformHeaderStillOpensTheStream(t *testing.T) {
	for _, platform := range []pb.Platform{
		pb.Platform_PLATFORM_DISCORD,
		pb.Platform_PLATFORM_MATRIX_PROTOCOL,
	} {
		t.Run(platform.String(), func(t *testing.T) {
			h := reverseHarness(t)

			stream := openWithHeader(t, h, platform.String())
			// A valid platform alone is not enough to be admitted: the caller
			// still has to resolve, which needs ginbot-user-id too.
			// reverseHarness registers reverseCallerUID on both platforms this
			// test drives.
			stream.RequestHeader().Set(callermeta.HeaderUserID, reverseCallerUID)

			if err := registrationVerdict(t, stream); err != nil {
				t.Errorf("the server refused a %v registration with %v", platform, err)
			}
		})
	}
}
