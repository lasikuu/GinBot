package interceptor

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// OriginInterceptor.WrapStreamingHandler stopped being a no-op this stage
// (stage 5, item B): TriggerService.GetFile is server-streaming now, and its
// visibility check (callerOriginInstanceID in pkg/grpc/server/trigger.go)
// depends on OriginFromContext being populated the same way it already is on
// the unary path. This file is the regression guard for that: without it, a
// streaming handler silently reverting to a no-op would not be caught by
// anything else in this package — every test in origin_test.go drives
// WrapUnary exclusively, and clearance_stream_test.go's
// fakeStreamingHandlerConn (reused here) exists for exactly this shape of gap.
//
// The end-to-end version of this — a GetFile call whose visibility can ONLY
// be explained by the caller's origin instance, not by ownership — lives in
// pkg/grpc/server/trigger_media_integration_test.go
// (TestGetFileIsVisibleThroughTheCallersOriginInstanceAlone), which needs a
// real database. What is tested here needs none: the mechanism itself, in
// isolation, is what a future regression would actually break first.

// streamOriginResult is everything one trip through the origin interceptor's
// STREAMING path produced, mirroring originResult (origin_test.go) for the
// unary one.
type streamOriginResult struct {
	reached  bool
	err      error
	origin   callermeta.Origin
	originOK bool
}

// streamOrigin runs one fake stream through the origin interceptor and
// reports what the wrapped handler observed, mirroring callOrigin
// (origin_test.go) for the streaming path. fakeStreamingHandlerConn and
// originTestCtx are both declared in this package's other _test.go files
// (clearance_stream_test.go, origin_test.go) and reused here rather than
// redeclared.
func streamOrigin(procedure string, header http.Header, resolve OriginResolver, caller *model.User) streamOriginResult {
	var result streamOriginResult

	handler := connect.StreamingHandlerFunc(func(handlerCtx context.Context, _ connect.StreamingHandlerConn) error {
		result.reached = true
		result.origin, result.originOK = OriginFromContext(handlerCtx)
		return nil
	})

	conn := newFakeStreamingHandlerConn(procedure)
	for key, values := range header {
		conn.header[key] = values
	}

	intercept := NewOriginInterceptor(resolve)
	result.err = intercept.WrapStreamingHandler(handler)(originTestCtx(header, caller), conn)

	return result
}

// TestOriginWrapStreamingHandlerPopulatesOriginFromContext is item 9's first
// half: a streaming call carrying origin headers must make OriginFromContext
// report that origin INSIDE the handler, exactly as WrapUnary already does
// (TestOriginIsBootstrappedForAResolvedCaller in origin_test.go).
func TestOriginWrapStreamingHandlerPopulatesOriginFromContext(t *testing.T) {
	resolver := &fakeOriginResolver{}
	origin := testOrigin()
	header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", origin)
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))

	got := streamOrigin(registeredMethod, header, resolver.resolve, caller)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Fatal("the streaming handler was not reached")
	}
	if !got.originOK {
		t.Fatal("OriginFromContext reported ok=false inside a streaming handler carrying origin headers")
	}
	if got.origin != origin {
		t.Errorf("origin = %+v, want %+v", got.origin, origin)
	}
	if n := resolver.callCount(); n != 1 {
		t.Errorf("resolver ran %d times, want exactly 1", n)
	}
}

// TestOriginWrapStreamingHandlerWritesNoRowWithoutOriginHeaders is item 9's
// second half: a stream carrying no origin headers at all — the reverse
// action stream's own shape, see OriginInterceptor.WrapStreamingHandler's own
// doc comment — must not write a destination row and must leave
// OriginFromContext reporting ok=false, exactly as the unary equivalent does
// (TestOriginIsNotBootstrappedWithoutOriginHeader in origin_test.go).
func TestOriginWrapStreamingHandlerWritesNoRowWithoutOriginHeaders(t *testing.T) {
	resolver := &fakeOriginResolver{}
	// Identity only, no HeaderInstanceUID/HeaderDestinationUID at all — what
	// OpenClientActionStream actually carries.
	header := wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "uid")
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))

	got := streamOrigin(registeredMethod, header, resolver.resolve, caller)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Fatal("the streaming handler was not reached")
	}
	if got.originOK {
		t.Errorf("OriginFromContext reported an origin (%+v) for a stream with no origin headers", got.origin)
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times for a stream with no origin headers, want 0: no destination row may be written", n)
	}
}
