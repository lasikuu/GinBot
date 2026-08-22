// Package callermeta defines how caller identity travels over gRPC metadata.
//
// Platform clients cannot pass identity as request fields, because the server
// needs it uniformly on every RPC. It travels as metadata instead, and this
// package owns both ends of that contract so the producer and consumer cannot
// disagree about the encoding — which they previously did: the Discord client
// sent the enum's name while the server parsed it with strconv.ParseInt, so
// every RPC that required caller identity failed with InvalidArgument.
package callermeta

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// KeyPlatformEnum carries the pb.Platform enum *name*, e.g. "PLATFORM_DISCORD".
	// Names are used rather than numbers because they are stable across proto
	// changes and legible when debugging the wire.
	KeyPlatformEnum = "platform_enum"

	// KeyUserID carries the platform-scoped user id, e.g. a Discord snowflake.
	// It is the platform's identifier, not the GinBot user_account UUID.
	KeyUserID = "user_id"
)

// Caller is the identity extracted from incoming metadata.
type Caller struct {
	PlatformEnum pb.Platform
	// PlatformUID is nil when the RPC was made without a user context.
	PlatformUID *string
}

// NewOutgoingContext attaches caller identity to an outgoing gRPC context.
func NewOutgoingContext(ctx context.Context, platform pb.Platform, platformUID string) context.Context {
	pairs := []string{KeyPlatformEnum, platform.String()}
	if platformUID != "" {
		pairs = append(pairs, KeyUserID, platformUID)
	}

	return metadata.NewOutgoingContext(ctx, metadata.Pairs(pairs...))
}

// FromIncomingContext extracts caller identity from an incoming gRPC context.
// The same key may carry multiple values; only the first is considered.
func FromIncomingContext(ctx context.Context) (*Caller, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "missing gRPC metadata")
	}

	platformValues := md.Get(KeyPlatformEnum)
	if len(platformValues) == 0 || platformValues[0] == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s is required", KeyPlatformEnum)
	}

	platformNumber, ok := pb.Platform_value[platformValues[0]]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unknown %s %q", KeyPlatformEnum, platformValues[0])
	}
	platform := pb.Platform(platformNumber)
	if platform == pb.Platform_PLATFORM_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "%s must not be unspecified", KeyPlatformEnum)
	}

	var platformUID *string
	if userValues := md.Get(KeyUserID); len(userValues) > 0 && userValues[0] != "" {
		platformUID = &userValues[0]
	}

	return &Caller{
		PlatformEnum: platform,
		PlatformUID:  platformUID,
	}, nil
}
