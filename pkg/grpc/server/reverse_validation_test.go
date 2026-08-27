package server

import (
	"context"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/grpc/codes"
)

// The stream validation interceptor, exercised on the one message it sees.
//
// Until OpenClientActionStreamReq carried constraints, the stream interceptor
// validated a message that could not fail: it was installed, it ran, and no
// input existed that it would reject. These tests are what make it observable.
//
// Everything here goes through newHarness, because the harness is the only place
// the interceptor chain actually runs — reverse_test.go calls the handler
// directly over a fakeStream and bypasses it entirely. The two files therefore
// cover two different lines of defence, and neither substitutes for the other.

// registrationVerdictGrace bounds how long a test waits for the stream to be
// refused before concluding it was accepted.
//
// There is no positive admission signal on the wire: a stream the server admits
// simply blocks in Recv, so "accepted" can only be inferred from the absence of a
// rejection, and inferring it takes a wait. 250ms against a measured sub-
// millisecond round trip over an in-process bufconn under -race. Too short would
// not produce a false PASS on the rejection tests — those block on Recv until the
// status arrives — only a false pass on the acceptance test, which is why the
// margin is generous rather than tight.
const registrationVerdictGrace = 250 * time.Millisecond

// registrationVerdict opens a client action stream, sends one registration and
// reports how the server answered.
//
// The error is read from Recv rather than from Send. A streaming Send is
// asynchronous — it hands the message to the transport and returns — so a
// rejection produced by the interceptor arrives as the stream's terminal status
// on the receive side, never as the send's return value. Asserting on Send would
// pass whatever the interceptor did.
//
// nil means the server did not reject the registration within the grace period.
func registrationVerdict(t *testing.T, h *harness, req *pb.OpenClientActionStreamReq) error {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancelled on the way out so an ACCEPTED stream's handler is released
	// rather than left parked in Recv for the rest of the binary.
	t.Cleanup(cancel)

	stream, err := h.Reverse.OpenClientActionStream(ctx)
	if err != nil {
		t.Fatalf("OpenClientActionStream: %v", err)
	}

	if err := stream.Send(req); err != nil {
		// A send that fails outright is not the rejection under test: the
		// interceptor rejects the message after the transport carried it, and
		// reporting that here would look identical to the test passing.
		t.Fatalf("send registration: %v", err)
	}

	// Buffered so this goroutine cannot be left parked on a send once the grace
	// period has expired and the test has moved on.
	ended := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
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

// TestAnUndefinedPlatformNumberIsRejectedByTheInterceptor is what
// enum.defined_only buys.
//
// A number no Platform value uses is a perfectly well-formed enum on the wire —
// protobuf enums are open, so it round-trips through the generated code without
// complaint and arrives at the handler as pb.Platform(99). The handler's own
// check does not catch it either: it tests for PLATFORM_UNSPECIFIED, and 99 is
// not that, so an unconstrained schema would register a client on a platform
// nothing routes to and leave it holding a registry slot indefinitely.
//
// InvalidArgument is therefore attributable: the handler returns
// ResourceExhausted, Internal or a context error, and never this.
func TestAnUndefinedPlatformNumberIsRejectedByTheInterceptor(t *testing.T) {
	h := newHarness(t)

	// 99 is chosen for being far outside the declared range (0..6) rather than
	// one past the end, so adding a Platform value cannot quietly make this test
	// stop testing anything.
	undefined := pb.Platform(99)
	if _, ok := pb.Platform_name[int32(undefined)]; ok {
		t.Fatalf("Platform(%d) is a defined value; pick a number the enum does not use", undefined)
	}

	err := registrationVerdict(t, h, pb.OpenClientActionStreamReq_builder{
		PlatformEnum: undefined.Enum(),
	}.Build())
	if err == nil {
		t.Fatalf("the server accepted a registration for Platform(%d); enum.defined_only is not being enforced",
			undefined)
	}
	requireCode(t, err, codes.InvalidArgument)
}

// TestAMissingPlatformIsRejectedByTheInterceptor is what required buys, and it
// is a DIFFERENT input from the one above on purpose.
//
// A test that only covered an undefined number would pass with required deleted,
// and a test that only covered an absent field would pass with defined_only
// deleted. Two rules, two inputs that each violate exactly one of them.
//
// An absent platform_enum is reachable: presence is explicit, so any client that
// simply does not set the field — trivially expressible in Go, and the default in
// several other languages' builders — sends this.
func TestAMissingPlatformIsRejectedByTheInterceptor(t *testing.T) {
	h := newHarness(t)

	// Nothing set at all. With explicit presence this is not serialised, so the
	// server genuinely sees an absent field rather than a zero one.
	req := pb.OpenClientActionStreamReq_builder{}.Build()
	if req.HasPlatformEnum() {
		t.Fatal("the fixture set platform_enum; this test is about the field being absent")
	}

	err := registrationVerdict(t, h, req)
	if err == nil {
		t.Fatal("the server accepted a registration with no platform_enum at all; required is not being enforced")
	}
	requireCode(t, err, codes.InvalidArgument)
}

// TestAnUnspecifiedPlatformIsRejectedByTheInterceptor.
//
// PLATFORM_UNSPECIFIED is the registration the server has no route for: there is
// no platform to fan actions out to, so admitting one costs a registry slot and
// a goroutine to deliver nothing.
//
// InvalidArgument is the assertion because it is attributable. The handler's own
// PLATFORM_UNSPECIFIED check does not produce an error at all — it logs and
// continues, leaving the stream open — so this code can only have come from the
// interceptor.
//
// What this test actually guards is the THIRD rule on platform_enum, which is
// the only one that does any work here. Neither of the other two rejects an
// explicitly set zero enum:
//
//   - required is a PRESENCE check. platform_enum has explicit presence, so a
//     client that sets it to PLATFORM_UNSPECIFIED serialises it, the server sees
//     it as present, and required is satisfied. Only an ABSENT field fails it,
//     which is what TestAMissingPlatformIsRejectedByTheInterceptor covers.
//   - enum.defined_only rejects numbers the enum does not declare. 0 IS declared
//     — it is PLATFORM_UNSPECIFIED — so it passes. That rule is covered by
//     TestAnUndefinedPlatformNumberIsRejectedByTheInterceptor.
//
// So deleting `(buf.validate.field).enum.not_in = 0` from reverse.proto would
// leave the other two rules in place, the whole reverse-stream suite passing,
// and this the only test that noticed. It was written while that rule was
// missing and failing on purpose; it is the regression guard now.
func TestAnUnspecifiedPlatformIsRejectedByTheInterceptor(t *testing.T) {
	h := newHarness(t)

	err := registrationVerdict(t, h, pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_UNSPECIFIED.Enum(),
	}.Build())
	if err == nil {
		t.Fatal("the interceptor accepted a PLATFORM_UNSPECIFIED registration: " +
			"required is a presence check and an explicitly set zero enum is present, " +
			"and enum.defined_only allows 0 because PLATFORM_UNSPECIFIED is a defined value. " +
			"(buf.validate.field).enum.not_in = 0 on OpenClientActionStreamReq.platform_enum " +
			"is the only rule that refuses this; check it is still there")
	}
	requireCode(t, err, codes.InvalidArgument)
}

// TestAValidPlatformStillOpensTheStream is the other half of every rejection
// above: constraints that refuse everything would satisfy all three and break
// the control channel outright.
func TestAValidPlatformStillOpensTheStream(t *testing.T) {
	for _, platform := range []pb.Platform{
		pb.Platform_PLATFORM_DISCORD,
		pb.Platform_PLATFORM_MATRIX_PROTOCOL,
	} {
		t.Run(platform.String(), func(t *testing.T) {
			h := newHarness(t)

			if err := registrationVerdict(t, h, pb.OpenClientActionStreamReq_builder{
				PlatformEnum: platform.Enum(),
			}.Build()); err != nil {
				t.Errorf("the server refused a %v registration with %v", platform, err)
			}
		})
	}
}
