// Package callermeta defines how caller identity and call origin travel over
// gRPC metadata.
//
// Identity here is *asserted by the platform client*, which is a trusted
// component. Nothing authenticates it: there is no token and no binding to the
// transport, so anything that can reach the gRPC port can claim to be any user,
// in any guild. Mutual TLS does not close that — it authenticates a bot process,
// not a Discord or Matrix user — and it is off unless GINBOT_GRPC_TLS is set.
//
// What metadata buys is uniformity, not authentication. Identity used to be
// available both here and as request fields; one channel means every RPC reads
// the caller through a single audited path, and this package owns both ends of
// the encoding so producer and consumer cannot disagree about it — which they
// previously did: the Discord client sent the enum's name while the server
// parsed it with strconv.ParseInt, so every RPC that required caller identity
// failed with InvalidArgument.
package callermeta

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	// KeyPlatformEnum carries the pb.Platform enum *name*, e.g. "PLATFORM_DISCORD".
	// Names are used rather than numbers because they are stable across proto
	// changes and legible when debugging the wire.
	KeyPlatformEnum = "platform_enum"

	// KeyUserID carries the platform-scoped user id, e.g. a Discord snowflake.
	// It is the platform's identifier, not the GinBot user_account UUID.
	KeyUserID = "user_id"

	// KeyInstanceUID carries the platform's identifier for the space a call came
	// from: a Discord guild, a Matrix homeserver's room space, and so on.
	KeyInstanceUID = "instance_uid"

	// KeyDestinationUID carries the platform's identifier for the channel or room
	// a call came from.
	KeyDestinationUID = "destination_uid"
)

// The jsonb field names used inside instance.instance_meta and
// destination.destination_meta.
//
// They spell the same words as the metadata headers above and are deliberately
// not defined in terms of them. The headers are a wire contract between the
// clients and this server, changeable by deploying both at once; these are a
// storage contract, indexed by uq_instance_platform_meta and matched by jsonb
// equality against rows already written. Renaming a header while the two shared
// a constant would orphan every existing instance row and silently start
// creating duplicates for guilds the bot already knows.
const (
	FieldInstanceUID    = "instance_uid"
	FieldDestinationUID = "destination_uid"
)

// Caller is the identity extracted from incoming metadata.
type Caller struct {
	PlatformEnum pb.Platform
	// PlatformUID is nil when the RPC was made without a user context.
	PlatformUID *string
}

// Origin is where on the platform a call came from. It is not identity: two
// different users share an origin when they type in the same channel.
//
// It travels as metadata for the same reason identity does: the server needs it
// uniformly on every RPC, through one path. Like identity it is asserted by the
// client and not verified.
type Origin struct {
	InstanceUID    string
	DestinationUID string
}

// InstanceMeta is the canonical jsonb shape of instance.instance_meta.
//
// The shape lives here rather than at each producer because instance rows are
// looked up by jsonb equality: two producers keying the same guild differently
// ({"guild_id": x} vs {"instance_uid": x}) would create two instance rows for
// one guild, and nothing would report the split.
func (o Origin) InstanceMeta() *structpb.Struct {
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			FieldInstanceUID: structpb.NewStringValue(o.InstanceUID),
		},
	}
}

// DestinationMeta is the canonical jsonb shape of destination.destination_meta.
// See InstanceMeta for why the shape is centralised.
func (o Origin) DestinationMeta() *structpb.Struct {
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			FieldDestinationUID: structpb.NewStringValue(o.DestinationUID),
		},
	}
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

// NewOutgoingOrigin appends origin metadata to an outgoing gRPC context.
//
// It must be called *after* NewOutgoingContext, not before: that function
// replaces the outgoing metadata wholesale, so origin set first would be
// discarded.
//
// An origin without an instance is dropped: a direct message belongs to no
// guild or room, and there is nothing to bootstrap for it.
func NewOutgoingOrigin(ctx context.Context, origin Origin) context.Context {
	if origin.InstanceUID == "" {
		return ctx
	}

	pairs := []string{KeyInstanceUID, origin.InstanceUID}
	if origin.DestinationUID != "" {
		pairs = append(pairs, KeyDestinationUID, origin.DestinationUID)
	}

	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

// OriginFromIncomingContext extracts the origin of a call.
//
// ok is false when the call carried none, which is normal rather than an error:
// direct messages and non-platform callers have no instance.
func OriginFromIncomingContext(ctx context.Context) (Origin, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Origin{}, false
	}

	instanceValues := md.Get(KeyInstanceUID)
	if len(instanceValues) == 0 || instanceValues[0] == "" {
		return Origin{}, false
	}

	origin := Origin{InstanceUID: instanceValues[0]}
	if destinationValues := md.Get(KeyDestinationUID); len(destinationValues) > 0 {
		origin.DestinationUID = destinationValues[0]
	}

	return origin, true
}
