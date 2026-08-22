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

// NewValidationStreamInterceptor enforces the same constraints on streaming
// RPCs.
//
// The unary interceptor alone leaves every streamed message unchecked, which
// covers the reverse action stream: OpenClientActionStreamReq arrives over a
// bidirectional stream and so never passed through validation at all.
func NewValidationStreamInterceptor() (grpc.StreamServerInterceptor, error) {
	validator, err := protovalidate.New()
	if err != nil {
		return nil, err
	}

	return func(
		srv any,
		stream grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		return handler(srv, &validatingServerStream{ServerStream: stream, validator: validator})
	}, nil
}

// validatingServerStream validates every message a handler receives.
//
// Only the receive side is wrapped. Outgoing messages are built by this server
// from its own data, so validating them would report a server bug to the client
// as InvalidArgument.
type validatingServerStream struct {
	grpc.ServerStream
	validator protovalidate.Validator
}

func (s *validatingServerStream) RecvMsg(m any) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}

	msg, ok := m.(proto.Message)
	if !ok {
		// Not a protobuf message; nothing to validate.
		return nil
	}

	if err := s.validator.Validate(msg); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	return nil
}
