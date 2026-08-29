package interceptor

import (
	"context"
	"maps"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// fakeStreamingHandlerConn is a minimal connect.StreamingHandlerConn: enough
// for an interceptor to read a procedure and headers from. Receive and Send
// are stubs, only there to satisfy the interface.
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

// streamCall is the streaming counterpart of call.
func streamCall(procedure string, header http.Header, reqs Requirements, resolve CallerResolver) callResult {
	var result callResult

	handler := connect.StreamingHandlerFunc(func(handlerCtx context.Context, _ connect.StreamingHandlerConn) error {
		result.reached = true
		result.caller, result.callerOK = CallerFromContext(handlerCtx)
		return nil
	})

	conn := newFakeStreamingHandlerConn(procedure)
	maps.Copy(conn.header, header)

	intercept := NewClearanceInterceptor(reqs, resolve)
	result.err = intercept.WrapStreamingHandler(handler)(context.Background(), conn)

	return result
}

func TestClearanceWrapStreamingHandlerRejectsAnUnauthorisedCaller(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := streamCall(registeredMethod, make(http.Header), testRequirements(), resolver.resolve)

	requireCode(t, got.err, connect.CodeInvalidArgument)
	if got.reached {
		t.Error("handler ran on a guarded stream with no identity headers at all")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times for a call with no identity, want 0", n)
	}
}

func TestClearanceWrapStreamingHandlerRejectsAnUnregisteredCaller(t *testing.T) {
	resolver := &recordingResolver{err: db.ErrNotFound}

	got := streamCall(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "unregistered"), testRequirements(), resolver.resolve)

	requireCode(t, got.err, connect.CodeFailedPrecondition)
	if got.reached {
		t.Error("handler ran for an unregistered caller")
	}
}

func TestClearanceWrapStreamingHandlerRejectsInsufficientClearance(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))}

	got := streamCall(adminMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "uid"), testRequirements(), resolver.resolve)

	requireCode(t, got.err, connect.CodePermissionDenied)
	if got.reached {
		t.Error("handler ran despite insufficient clearance")
	}
}

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
