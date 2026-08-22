package interceptor

import (
	"context"
	"runtime/debug"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoverUnaryInterceptor turns a handler panic into codes.Internal.
//
// grpc-go does not recover on behalf of a handler, so a nil dereference in one
// of them takes the whole server process down with it — every in-flight request
// and every open reverse stream included. Chain it outermost so it also covers
// the other interceptors.
//
// The recovered value can carry internal detail, so it goes to the log and the
// caller gets a bare Internal.
func RecoverUnaryInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (resp any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logRecovered(info.FullMethod, recovered)
			resp = nil
			err = status.Error(codes.Internal, "internal error")
		}
	}()

	return handler(ctx, req)
}

// RecoverStreamInterceptor is the streaming equivalent. A panic raised while a
// reverse action stream is being served would otherwise be just as fatal.
func RecoverStreamInterceptor(
	srv any,
	stream grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logRecovered(info.FullMethod, recovered)
			err = status.Error(codes.Internal, "internal error")
		}
	}()

	return handler(srv, stream)
}

// logRecovered records the panic. The stack is included because the recovered
// value on its own — often a bare "runtime error: invalid memory address" —
// does not say which handler produced it.
func logRecovered(method string, recovered any) {
	log.Z.Error("recovered from a panic in a gRPC handler",
		zap.String("method", method),
		zap.Any("panic", recovered),
		zap.ByteString("stack", debug.Stack()),
	)
}
