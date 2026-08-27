package interceptor

import (
	"context"
	"errors"
	"runtime/debug"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// RecoverInterceptor turns a handler panic into connect.CodeInternal.
//
// Connect does not recover on behalf of a handler, so a nil dereference in one
// of them takes the whole server process down with it — every in-flight
// request and every open reverse stream included. Chain it outermost so it
// also covers the other interceptors.
//
// The recovered value can carry internal detail, so it goes to the log and the
// caller gets a bare Internal.
type RecoverInterceptor struct{}

// WrapUnary implements connect.Interceptor.
func (RecoverInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logRecovered(req.Spec().Procedure, recovered)
				resp = nil
				err = connect.NewError(connect.CodeInternal, errors.New("internal error"))
			}
		}()

		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op. Recovery is a server-side concern here:
// wrapping the client half would apply it to outgoing calls this process
// makes, and this server makes none through its own interceptor chain.
func (RecoverInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler is the streaming equivalent. A panic raised while a
// reverse action stream is being served would otherwise be just as fatal.
//
// It covers the goroutine running next(ctx, conn), and only that one. The
// qualification is not pedantry: OpenClientActionStream runs its own
// conn.Receive() on a goroutine of its own, which no frame here unwinds, so
// that goroutine carries its own recover. Anything else a stream handler
// spawns has to do the same.
func (RecoverInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logRecovered(conn.Spec().Procedure, recovered)
				err = connect.NewError(connect.CodeInternal, errors.New("internal error"))
			}
		}()

		return next(ctx, conn)
	}
}

// logRecovered records the panic. The stack is included because the recovered
// value on its own — often a bare "runtime error: invalid memory address" —
// does not say which handler produced it.
func logRecovered(procedure string, recovered any) {
	log.Z.Error("recovered from a panic in a Connect handler",
		zap.String("procedure", procedure),
		zap.Any("panic", recovered),
		zap.ByteString("stack", debug.Stack()),
	)
}
