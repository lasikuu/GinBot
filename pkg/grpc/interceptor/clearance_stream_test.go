package interceptor

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/grpc/codes"
)

// ClearanceInterceptor.WrapStreamingHandler is new in this stage:
// OpenClientActionStream previously had no identity check at all, because a
// unary-only interceptor cannot wrap it (see ADR-0012 and the comment on
// requirements.go). connect.StreamingHandlerConn is an ordinary exported
// interface with no sealed methods — unlike connect.BidiStream itself, which
// has no public constructor outside connectrpc.com/connect — so it can be
// faked directly here, which is what makes a pure, transport-free unit test
// of the streaming path possible at all.

// fakeStreamingHandlerConn is a minimal connect.StreamingHandlerConn: enough
// for ClearanceInterceptor.WrapStreamingHandler to read a procedure and
// headers from, and nothing else. It is not a stream — Receive/Send are
// unused by the interceptor and are stubbed only to satisfy the interface.
type fakeStreamingHandlerConn struct {
	procedure string
	header    http.Header
}

func newFakeStreamingHandlerConn(procedure string) *fakeStreamingHandlerConn {
	return &fakeStreamingHandlerConn{procedure: procedure, header: make(http.Header)}
}

func (f *fakeStreamingHandlerConn) Spec() connect.Spec {
	return connect.Spec{Procedure: f.procedure, StreamType: connect.StreamTypeBidi}
}
func (f *fakeStreamingHandlerConn) Peer() connect.Peer           { return connect.Peer{} }
func (f *fakeStreamingHandlerConn) Receive(any) error            { return nil }
func (f *fakeStreamingHandlerConn) RequestHeader() http.Header   { return f.header }
func (f *fakeStreamingHandlerConn) Send(any) error               { return nil }
func (f *fakeStreamingHandlerConn) ResponseHeader() http.Header  { return make(http.Header) }
func (f *fakeStreamingHandlerConn) ResponseTrailer() http.Header { return make(http.Header) }

// streamCall runs one fake stream through the clearance interceptor and
// reports what the wrapped handler observed, mirroring callResult for the
// unary path.
func streamCall(procedure string, header http.Header, reqs Requirements, resolve CallerResolver) callResult {
	var result callResult

	handler := connect.StreamingHandlerFunc(func(handlerCtx context.Context, _ connect.StreamingHandlerConn) error {
		result.reached = true
		result.caller, result.callerOK = CallerFromContext(handlerCtx)
		return nil
	})

	conn := newFakeStreamingHandlerConn(procedure)
	for key, values := range header {
		conn.header[key] = values
	}

	intercept := NewClearanceInterceptor(reqs, resolve)
	result.err = intercept.WrapStreamingHandler(handler)(context.Background(), conn)

	return result
}

// TestClearanceWrapStreamingHandlerRejectsAnUnauthorisedCaller is the
// assertion that was impossible before this stage: a stream that never
// carries identity headers at all must never reach the handler.
func TestClearanceWrapStreamingHandlerRejectsAnUnauthorisedCaller(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := streamCall(registeredMethod, make(http.Header), testRequirements(), resolver.resolve)

	requireCode(t, got.err, codes.InvalidArgument)
	if got.reached {
		t.Error("handler ran on a guarded stream with no identity headers at all")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times for a call with no identity, want 0", n)
	}
}

// TestClearanceWrapStreamingHandlerRejectsAnUnregisteredCaller: well-formed
// identity, but the resolver reports no matching account.
func TestClearanceWrapStreamingHandlerRejectsAnUnregisteredCaller(t *testing.T) {
	resolver := &recordingResolver{err: db.ErrNotFound}

	got := streamCall(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "unregistered"), testRequirements(), resolver.resolve)

	requireCode(t, got.err, codes.FailedPrecondition)
	if got.reached {
		t.Error("handler ran for an unregistered caller")
	}
}

// TestClearanceWrapStreamingHandlerRejectsInsufficientClearance mirrors
// TestClearanceBoundaries for the streaming path, at one representative gate.
func TestClearanceWrapStreamingHandlerRejectsInsufficientClearance(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))}

	got := streamCall(adminMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "uid"), testRequirements(), resolver.resolve)

	requireCode(t, got.err, codes.PermissionDenied)
	if got.reached {
		t.Error("handler ran despite insufficient clearance")
	}
}

// TestClearanceWrapStreamingHandlerAdmitsARegisteredCaller: the positive
// case. The resolved caller must reach the handler exactly as it does on the
// unary path, retrievable through the same CallerFromContext.
func TestClearanceWrapStreamingHandlerAdmitsARegisteredCaller(t *testing.T) {
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))
	resolver := &recordingResolver{user: caller}

	got := streamCall(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "uid"), testRequirements(), resolver.resolve)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Fatal("handler was not reached")
	}
	if !got.callerOK || got.caller != caller {
		t.Errorf("CallerFromContext = %+v, %v; want the resolved caller", got.caller, got.callerOK)
	}
}

// TestClearanceWrapStreamingHandlerPublicProcedureResolvesNobody: a stream
// procedure absent from the map must behave exactly as a public unary method
// does — reachable with no identity and no resolver call.
func TestClearanceWrapStreamingHandlerPublicProcedureResolvesNobody(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := streamCall(publicMethod, make(http.Header), testRequirements(), resolver.resolve)

	if got.err != nil {
		t.Fatalf("public procedure rejected: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times for a public stream procedure, want 0", n)
	}
}
