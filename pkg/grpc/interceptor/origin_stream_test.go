package interceptor

import (
	"context"
	"maps"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// Guards against OriginInterceptor.WrapStreamingHandler reverting to a no-op:
// streaming GetFile's visibility check depends on OriginFromContext.
type streamOriginResult struct {
	reached  bool
	err      error
	origin   callermeta.Origin
	originOK bool
}

// streamOrigin is the streaming counterpart of callOrigin.
func streamOrigin(procedure string, header http.Header, resolve OriginResolver, caller *model.User) streamOriginResult {
	var result streamOriginResult

	handler := connect.StreamingHandlerFunc(func(handlerCtx context.Context, _ connect.StreamingHandlerConn) error {
		result.reached = true
		result.origin, result.originOK = OriginFromContext(handlerCtx)
		return nil
	})

	conn := newFakeStreamingHandlerConn(procedure)
	maps.Copy(conn.header, header)

	intercept := NewOriginInterceptor(resolve)
	result.err = intercept.WrapStreamingHandler(handler)(originTestCtx(header, caller), conn)

	return result
}

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

func TestOriginWrapStreamingHandlerWritesNoRowWithoutOriginHeaders(t *testing.T) {
	resolver := &fakeOriginResolver{}
	// Identity only, which is what OpenClientActionStream carries.
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
