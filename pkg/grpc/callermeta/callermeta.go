// Package callermeta defines how caller identity and call origin travel to
// ginbot-server: as Connect/HTTP request headers on the wire, and as context
// values on the client side that produce them.
//
// Identity here is *asserted by the platform client*, which is a trusted
// component. Nothing authenticates it: there is no token and no binding to the
// transport, so anything that can reach the Connect port can claim to be any
// user, in any guild. Mutual TLS does not close that — it authenticates a bot
// process, not a Discord or Matrix user — and it is off unless GINBOT_GRPC_TLS
// is set.
//
// What the header contract buys is uniformity, not authentication. Identity
// used to be available both as headers and as request fields; one channel
// means every RPC reads the caller through a single audited path, and this
// package owns both ends of the encoding so producer and consumer cannot
// disagree about it — which they previously did: the Discord client sent the
// enum's name while the server parsed it with strconv.ParseInt, so every RPC
// that required caller identity failed with InvalidArgument.
package callermeta

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// The header names identity and origin travel under.
//
// They are hyphenated and prefixed rather than snake_cased because that is
// what they actually are: ordinary HTTP/2 header fields, lowercased on the
// wire, and any transport or proxy in front of them treats them as such —
// where an underscore is legal but unidiomatic and is dropped outright by
// some proxies. The ginbot- prefix keeps them from colliding with anything
// standard.
//
// This is a WIRE contract and renaming it is safe: server and clients are
// deployed together, and both ends of the encoding live in this package. Renaming
// the jsonb group below is not safe — see the comment there.
const (
	// HeaderPlatformEnum carries the pb.Platform enum *name*, e.g. "PLATFORM_DISCORD".
	// Names are used rather than numbers because they are stable across proto
	// changes and legible when debugging the wire.
	HeaderPlatformEnum = "ginbot-platform-enum"

	// HeaderUserID carries the platform-scoped user id, e.g. a Discord snowflake.
	// It is the platform's identifier, not the GinBot user_account UUID.
	HeaderUserID = "ginbot-user-id"

	// HeaderInstanceUID carries the platform's identifier for the space a call
	// came from: a Discord guild, a Matrix homeserver's room space, and so on.
	HeaderInstanceUID = "ginbot-instance-uid"

	// HeaderDestinationUID carries the platform's identifier for the channel or
	// room a call came from.
	HeaderDestinationUID = "ginbot-destination-uid"
)

// The jsonb field names used inside instance.instance_meta and
// destination.destination_meta.
//
// They name the same two things as the headers above and are deliberately not
// defined in terms of them. The headers are a wire contract between the clients
// and this server, changeable by deploying both at once; these are a STORAGE
// contract, indexed by uq_instance_platform_meta and matched by jsonb equality
// against rows already written.
//
// The header rename above is exactly the event this separation exists for. Had
// the two shared a constant, that rename would have orphaned every existing
// instance row and silently started creating duplicates for guilds the bot
// already knows — no error, no log line, triggers and reminders simply stopping
// resolving. DO NOT rename these, and do not "tidy up" the duplication by
// deriving one group from the other.
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
//
// It travels as a header for the same reason identity does: the server needs it
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

// callerContextKey and originContextKey type the context values NewOutgoingContext
// and NewOutgoingOrigin attach, so they cannot collide with another package's
// key. WriteHeader is what turns them into the actual request headers, at the
// point a call is finally sent — see NewClientInterceptor.
type callerContextKey struct{}
type originContextKey struct{}

// outgoingCaller is the context value NewOutgoingContext stores. It is a
// plain (platform, uid) pair rather than *Caller, so it can carry an empty
// platformUID without a caller having to decide whether nil or "" means
// "none" — WriteHeader is the single place that decision is made, matching
// the outgoing gRPC metadata behaviour this replaces.
type outgoingCaller struct {
	platform    pb.Platform
	platformUID string
}

// NewOutgoingContext attaches caller identity to ctx, to be written onto
// request headers by NewClientInterceptor when a call is actually made.
func NewOutgoingContext(ctx context.Context, platform pb.Platform, platformUID string) context.Context {
	return context.WithValue(ctx, callerContextKey{}, outgoingCaller{platform: platform, platformUID: platformUID})
}

// NewOutgoingOrigin attaches origin to ctx, to be written onto request
// headers by NewClientInterceptor when a call is actually made.
//
// Unlike the gRPC metadata API this replaced, calling it before or after
// NewOutgoingContext no longer matters: each attaches its OWN context value
// under its own key, so neither call can discard what the other set. The
// two used to share one underlying metadata map that
// metadata.NewOutgoingContext replaced wholesale, which made the order load-
// bearing; context.WithValue values simply accumulate, so that hazard is
// gone.
//
// An origin without an instance is dropped: a direct message belongs to no
// guild or room, and there is nothing to bootstrap for it.
func NewOutgoingOrigin(ctx context.Context, origin Origin) context.Context {
	if origin.InstanceUID == "" {
		return ctx
	}

	return context.WithValue(ctx, originContextKey{}, origin)
}

// WriteHeader writes whatever caller identity and origin ctx carries onto h.
//
// A ctx carrying neither writes nothing: an RPC made with no identity
// attached is deliberately anonymous (UtilityService/Ping, for instance), not
// an error to guard against here.
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

// NewClientInterceptor returns a connect.Interceptor that copies caller
// identity and origin from the context onto outgoing request headers, on both
// unary calls and streaming clients.
//
// This is what actually turns NewOutgoingContext/NewOutgoingOrigin's context
// values into wire headers; pkg/grpc/client.Dial installs it on every
// generated client this process builds; see the ordering note there.
func NewClientInterceptor() connect.Interceptor {
	return &clientInterceptor{}
}

type clientInterceptor struct{}

// WrapUnary implements connect.Interceptor.
func (clientInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		WriteHeader(ctx, req.Header())
		return next(ctx, req)
	}
}

// WrapStreamingClient sets the headers via conn.RequestHeader() on the
// returned connect.StreamingClientConn — a streaming call has no per-message
// AnyRequest for WrapUnary's approach to reach, only the connection's own
// header set, sent once with the opening request.
func (clientInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		WriteHeader(ctx, conn.RequestHeader())
		return conn
	}
}

// WrapStreamingHandler is a no-op: this is a client-side interceptor, and
// this process never mounts a handler that would need it.
func (clientInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// FromHeader extracts caller identity from Connect request headers.
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

// OriginFromHeader extracts the origin of a call from Connect request
// headers.
//
// ok is false when the call carried none, which is normal rather than an
// error: direct messages and non-platform callers have no instance.
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
