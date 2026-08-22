// Package interceptor holds server-side gRPC interceptors.
package interceptor

import (
	"context"

	"buf.build/go/protovalidate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// NewValidationUnaryInterceptor enforces the buf.validate constraints declared
// in the .proto files.
//
// Without this the constraints are inert: reminder.proto has carried rules such
// as required datetime, gt_now and the repeat_cron pattern from the start, and
// nothing ever evaluated them.
func NewValidationUnaryInterceptor() (grpc.UnaryServerInterceptor, error) {
	validator, err := protovalidate.New()
	if err != nil {
		return nil, err
	}

	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		msg, ok := req.(proto.Message)
		if !ok {
			// Not a protobuf message; nothing to validate.
			return handler(ctx, req)
		}

		if err := validator.Validate(msg); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		return handler(ctx, req)
	}, nil
}
