// Package callermeta encodes caller identity and call origin as Connect request
// headers. Identity is asserted by the platform client and is not authenticated:
// anything that can reach the Connect port can claim to be any user.
package callermeta

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// Wire header names. HeaderPlatformEnum carries the pb.Platform enum name
// (e.g. "PLATFORM_DISCORD"); HeaderUserID carries the platform-scoped user id,
// not the GinBot user_account UUID.
const (
	HeaderPlatformEnum = "ginbot-platform-enum"

	HeaderUserID = "ginbot-user-id"

	HeaderInstanceUID = "ginbot-instance-uid"

	HeaderDestinationUID = "ginbot-destination-uid"
)

// jsonb keys inside instance.instance_meta and destination.destination_meta,
// matched by equality against existing rows. Never derive these from Header*.
const (
	FieldInstanceUID    = "instance_uid"
	FieldDestinationUID = "destination_uid"
)

// Caller is the identity extracted from incoming request headers.
type Caller struct {
	PlatformEnum pb.Platform
	// PlatformUID is nil when the RPC was made without a user context.
	PlatformUID *string
}

// Origin is where on the platform a call came from. It is not identity: two
// different users share an origin when they type in the same channel.
type Origin struct {
	InstanceUID    string
	DestinationUID string
}

// InstanceMeta is the canonical jsonb shape of instance.instance_meta. Rows are
// looked up by jsonb equality, so producers must not key a guild differently.
func (o Origin) InstanceMeta() *structpb.Struct {
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			FieldInstanceUID: structpb.NewStringValue(o.InstanceUID),
		},
	}
}

// DestinationMeta is the canonical jsonb shape of destination.destination_meta.
func (o Origin) DestinationMeta() *structpb.Struct {
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			FieldDestinationUID: structpb.NewStringValue(o.DestinationUID),
		},
	}
}

type callerContextKey struct{}
type originContextKey struct{}

type outgoingCaller struct {
	platform    pb.Platform
	platformUID string
}

// NewOutgoingContext attaches caller identity to ctx, written onto request
// headers by NewClientInterceptor when a call is made.
func NewOutgoingContext(ctx context.Context, platform pb.Platform, platformUID string) context.Context {
	return context.WithValue(ctx, callerContextKey{}, outgoingCaller{platform: platform, platformUID: platformUID})
}

// NewOutgoingOrigin attaches origin to ctx. An origin with no InstanceUID is
// dropped: a direct message belongs to no guild or room.
func NewOutgoingOrigin(ctx context.Context, origin Origin) context.Context {
	if origin.InstanceUID == "" {
		return ctx
	}

	return context.WithValue(ctx, originContextKey{}, origin)
}

// WriteHeader writes whatever caller identity and origin ctx carries onto h.
// A ctx carrying neither writes nothing; an anonymous RPC is not an error here.
func WriteHeader(ctx context.Context, h http.Header) {
	if caller, ok := ctx.Value(callerContextKey{}).(outgoingCaller); ok {
		h.Set(HeaderPlatformEnum, caller.platform.String())
		if caller.platformUID != "" {
			h.Set(HeaderUserID, caller.platformUID)
		}
	}

	if origin, ok := ctx.Value(originContextKey{}).(Origin); ok {
		h.Set(HeaderInstanceUID, origin.InstanceUID)
		if origin.DestinationUID != "" {
			h.Set(HeaderDestinationUID, origin.DestinationUID)
		}
	}
}

// NewClientInterceptor returns a connect.Interceptor that copies caller identity
// and origin from the context onto outgoing request headers.
func NewClientInterceptor() connect.Interceptor {
	return &clientInterceptor{}
}

type clientInterceptor struct{}

func (clientInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		WriteHeader(ctx, req.Header())
		return next(ctx, req)
	}
}

// WrapStreamingClient uses conn.RequestHeader(): a stream has no per-message
// AnyRequest, only the connection headers sent with the opening request.
func (clientInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		WriteHeader(ctx, conn.RequestHeader())
		return conn
	}
}

// WrapStreamingHandler is a no-op: this interceptor is client-side only.
func (clientInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func FromHeader(header http.Header) (*Caller, error) {
	platformValue := header.Get(HeaderPlatformEnum)
	if platformValue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is required", HeaderPlatformEnum))
	}

	platformNumber, ok := pb.Platform_value[platformValue]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown %s %q", HeaderPlatformEnum, platformValue))
	}
	platform := pb.Platform(platformNumber)
	if platform == pb.Platform_PLATFORM_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s must not be unspecified", HeaderPlatformEnum))
	}

	var platformUID *string
	if userValue := header.Get(HeaderUserID); userValue != "" {
		platformUID = &userValue
	}

	return &Caller{
		PlatformEnum: platform,
		PlatformUID:  platformUID,
	}, nil
}

// OriginFromHeader returns ok false when the call carried no origin, which is
// normal: direct messages and non-platform callers have no instance.
func OriginFromHeader(header http.Header) (Origin, bool) {
	instanceValue := header.Get(HeaderInstanceUID)
	if instanceValue == "" {
		return Origin{}, false
	}

	return Origin{
		InstanceUID:    instanceValue,
		DestinationUID: header.Get(HeaderDestinationUID),
	}, true
}
