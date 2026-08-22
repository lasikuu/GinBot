package callermeta

import (
	"context"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// incoming converts an outgoing context into an incoming one, simulating the
// round trip over the wire so both halves of the contract are exercised together.
func incoming(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("no outgoing metadata attached")
	}
	return metadata.NewIncomingContext(context.Background(), md)
}

// This is the regression test for the original break: the client sent the enum
// name while the server parsed it as an integer, so nothing round-tripped.
func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		platform pb.Platform
		uid      string
		wantUID  bool
	}{
		{"discord with user", pb.Platform_PLATFORM_DISCORD, "123456789", true},
		{"matrix with user", pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org", true},
		{"discord without user", pb.Platform_PLATFORM_DISCORD, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewOutgoingContext(context.Background(), tt.platform, tt.uid)

			caller, err := FromIncomingContext(incoming(t, ctx))
			if err != nil {
				t.Fatalf("FromIncomingContext: %v", err)
			}
			if caller.PlatformEnum != tt.platform {
				t.Errorf("platform = %v, want %v", caller.PlatformEnum, tt.platform)
			}
			if tt.wantUID {
				if caller.PlatformUID == nil {
					t.Fatal("PlatformUID = nil, want a value")
				}
				if *caller.PlatformUID != tt.uid {
					t.Errorf("PlatformUID = %q, want %q", *caller.PlatformUID, tt.uid)
				}
			} else if caller.PlatformUID != nil {
				t.Errorf("PlatformUID = %q, want nil", *caller.PlatformUID)
			}
		})
	}
}

func TestOriginRoundTrip(t *testing.T) {
	tests := []struct {
		name            string
		origin          Origin
		wantOK          bool
		wantInstance    string
		wantDestination string
	}{
		{
			name:            "guild and channel",
			origin:          Origin{InstanceUID: "guild-1", DestinationUID: "channel-1"},
			wantOK:          true,
			wantInstance:    "guild-1",
			wantDestination: "channel-1",
		},
		{
			name:         "instance only",
			origin:       Origin{InstanceUID: "guild-1"},
			wantOK:       true,
			wantInstance: "guild-1",
		},
		{
			// A direct message belongs to no guild, so there is nothing to
			// bootstrap and the origin is dropped rather than half-sent.
			name:   "direct message has no instance",
			origin: Origin{DestinationUID: "dm-channel"},
			wantOK: false,
		},
		{
			name:   "empty origin",
			origin: Origin{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewOutgoingContext(context.Background(), pb.Platform_PLATFORM_DISCORD, "uid")
			ctx = NewOutgoingOrigin(ctx, tt.origin)

			got, ok := OriginFromIncomingContext(incoming(t, ctx))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (origin = %+v)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.InstanceUID != tt.wantInstance {
				t.Errorf("InstanceUID = %q, want %q", got.InstanceUID, tt.wantInstance)
			}
			if got.DestinationUID != tt.wantDestination {
				t.Errorf("DestinationUID = %q, want %q", got.DestinationUID, tt.wantDestination)
			}
		})
	}
}

// Attaching the origin must not cost the identity that was attached first.
func TestOriginAndIdentityCoexist(t *testing.T) {
	ctx := NewOutgoingContext(context.Background(), pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org")
	ctx = NewOutgoingOrigin(ctx, Origin{InstanceUID: "!room:example.org", DestinationUID: "!room:example.org"})

	incomingCtx := incoming(t, ctx)

	caller, err := FromIncomingContext(incomingCtx)
	if err != nil {
		t.Fatalf("FromIncomingContext: %v", err)
	}
	if caller.PlatformEnum != pb.Platform_PLATFORM_MATRIX_PROTOCOL {
		t.Errorf("platform = %v, want %v", caller.PlatformEnum, pb.Platform_PLATFORM_MATRIX_PROTOCOL)
	}
	if caller.PlatformUID == nil || *caller.PlatformUID != "@a:example.org" {
		t.Errorf("PlatformUID = %v, want @a:example.org", caller.PlatformUID)
	}

	if _, ok := OriginFromIncomingContext(incomingCtx); !ok {
		t.Error("origin was lost when identity was attached")
	}
}

// This documents a trap; it does not endorse it. NewOutgoingContext calls
// metadata.NewOutgoingContext, which REPLACES the outgoing metadata wholesale,
// so an origin appended first is silently discarded. Swapping the two lines in
// discord.commandContext disables origin bootstrap forever and every other test
// in the repository still passes — this is the one that would not.
func TestOriginIsLostWhenAttachedBeforeIdentity(t *testing.T) {
	origin := Origin{InstanceUID: "guild-1", DestinationUID: "channel-1"}

	// The wrong order.
	ctx := NewOutgoingOrigin(context.Background(), origin)
	ctx = NewOutgoingContext(ctx, pb.Platform_PLATFORM_DISCORD, "uid")

	if got, ok := OriginFromIncomingContext(incoming(t, ctx)); ok {
		t.Fatalf("origin survived the wrong ordering as %+v; if this now works, "+
			"delete this test and the ordering warning on NewOutgoingOrigin", got)
	}

	// The right order, for contrast: same two calls, same arguments.
	right := NewOutgoingContext(context.Background(), pb.Platform_PLATFORM_DISCORD, "uid")
	right = NewOutgoingOrigin(right, origin)

	if _, ok := OriginFromIncomingContext(incoming(t, right)); !ok {
		t.Error("origin was lost even in the documented order")
	}
}

// The jsonb shapes are matched by equality against rows already written, so
// they are a storage contract. A change here orphans every existing instance
// row and silently starts creating duplicates.
func TestMetaShapesAreCanonical(t *testing.T) {
	origin := Origin{InstanceUID: "guild-1", DestinationUID: "channel-1"}

	tests := []struct {
		name  string
		meta  *structpb.Struct
		field string
		want  string
	}{
		{"instance", origin.InstanceMeta(), FieldInstanceUID, origin.InstanceUID},
		{"destination", origin.DestinationMeta(), FieldDestinationUID, origin.DestinationUID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(tt.meta.GetFields()); got != 1 {
				t.Fatalf("meta has %d fields, want exactly 1: %v", got, tt.meta.GetFields())
			}
			value, ok := tt.meta.GetFields()[tt.field]
			if !ok {
				t.Fatalf("meta has no %q field: %v", tt.field, tt.meta.GetFields())
			}
			if got := value.GetStringValue(); got != tt.want {
				t.Errorf("meta[%s] = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

// The jsonb keys and the gRPC header names are two independent contracts that
// happen to spell the same words. Pinned as literals: a rename of either must
// be a deliberate, visible change here rather than something a shared constant
// carries across silently.
func TestStorageAndHeaderKeysArePinned(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"header instance_uid", KeyInstanceUID, "instance_uid"},
		{"header destination_uid", KeyDestinationUID, "destination_uid"},
		{"header platform_enum", KeyPlatformEnum, "platform_enum"},
		{"header user_id", KeyUserID, "user_id"},
		{"jsonb instance_uid", FieldInstanceUID, "instance_uid"},
		{"jsonb destination_uid", FieldDestinationUID, "destination_uid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("= %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestFromIncomingContextErrors(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{
			name: "no metadata at all",
			ctx:  context.Background(),
		},
		{
			name: "platform_enum missing",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(KeyUserID, "1")),
		},
		{
			name: "platform_enum empty",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(KeyPlatformEnum, "")),
		},
		{
			name: "platform_enum unknown name",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(KeyPlatformEnum, "PLATFORM_CARRIER_PIGEON")),
		},
		{
			// The pre-fix client/server mismatch: a numeric value is not a valid name.
			name: "platform_enum sent as a number",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(KeyPlatformEnum, "1")),
		},
		{
			name: "platform_enum unspecified",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(KeyPlatformEnum, pb.Platform_PLATFORM_UNSPECIFIED.String())),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := FromIncomingContext(tt.ctx); err == nil {
				t.Fatal("expected an error")
			} else if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
			}
		})
	}
}
