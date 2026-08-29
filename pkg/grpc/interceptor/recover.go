package interceptor

import (
	"context"
	"errors"
	"runtime/debug"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// RecoverInterceptor turns a handler panic into connect.CodeInternal; Connect
// does not recover on its own. Chain it outermost to cover the other
// interceptors. The recovered value is logged, never returned to the caller.
type RecoverInterceptor struct{}

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

// WrapStreamingClient is a no-op: recovery is server-side only.
func (RecoverInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler covers only the goroutine running next(ctx, conn); a
// handler that spawns goroutines must recover in each of them itself.
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

func logRecovered(procedure string, recovered any) {
	log.Z.Error("recovered from a panic in a Connect handler",
		zap.String("procedure", procedure),
		zap.Any("panic", recovered),
		zap.ByteString("stack", debug.Stack()),
	)
}
