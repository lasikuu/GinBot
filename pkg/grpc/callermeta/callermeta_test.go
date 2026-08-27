package callermeta

import (
	"context"
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
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
//
// It is also the guard for the header rename. This package owns both ends of
// the encoding, so a rename that touched only the producer or only the consumer
// would fail here — and nowhere else, until every RPC needing caller identity
// started answering InvalidArgument in production, which is exactly how the
// original break was found.
//
// The wire form is asserted alongside the decoded result: a round trip that
// only checked what came back out would still pass if both ends had moved to
// some third spelling together.
func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		platform pb.Platform
		uid      string
		wantUID  bool
	}{
		{"discord with user", pb.Platform_PLATFORM_DISCORD, "123456789", true},
		{"matrix with user", pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org", true},
		// No user id: the client is acting on its own behalf, which is normal
		// for cron-driven and health traffic. The header must simply be absent,
		// not present and empty.
		{"discord without user", pb.Platform_PLATFORM_DISCORD, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewOutgoingContext(context.Background(), tt.platform, tt.uid)

			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata attached")
			}
			if got := md.Get(HeaderPlatformEnum); len(got) != 1 || got[0] != tt.platform.String() {
				t.Errorf("outgoing %q = %v, want [%q]", HeaderPlatformEnum, got, tt.platform.String())
			}
			if userValues := md.Get(HeaderUserID); tt.wantUID {
				if len(userValues) != 1 || userValues[0] != tt.uid {
					t.Errorf("outgoing %q = %v, want [%q]", HeaderUserID, userValues, tt.uid)
				}
			} else if len(userValues) != 0 {
				t.Errorf("outgoing %q = %v, want it absent entirely", HeaderUserID, userValues)
			}

			// No origin was attached, and none must appear.
			if got, ok := OriginFromIncomingContext(incoming(t, ctx)); ok {
				t.Errorf("origin = %+v on a call that carried none", got)
			}

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

			// The wire form, for the same reason as in TestRoundTrip: both ends
			// of this encoding live here, so agreeing with each other on the
			// wrong header name would otherwise go unnoticed.
			md, hasMetadata := metadata.FromOutgoingContext(ctx)
			if !hasMetadata {
				t.Fatal("no outgoing metadata attached")
			}
			if values := md.Get(HeaderInstanceUID); tt.wantOK {
				if len(values) != 1 || values[0] != tt.wantInstance {
					t.Errorf("outgoing %q = %v, want [%q]", HeaderInstanceUID, values, tt.wantInstance)
				}
			} else if len(values) != 0 {
				t.Errorf("outgoing %q = %v, want it absent entirely", HeaderInstanceUID, values)
			}

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

// The jsonb field names are a STORAGE contract and are pinned as literals here
// on purpose. See the comment block above them in callermeta.go.
//
// instance.instance_meta and destination.destination_meta are matched by jsonb
// equality against rows that already exist, indexed by
// uq_instance_platform_meta. Renaming either key does not fail: it silently
// stops matching. Every guild the bot already knows gets a second instance row,
// its triggers and reminders stop resolving, nothing logs an error, and the
// Ruby-bot migration currently in flight keeps writing the old shape the whole
// time.
//
// This is deliberately cheap insurance against a tidy-up pass — exactly the
// kind that just renamed the header group two constants above. The next person
// doing one needs a test that stops them, not a comment they might read.
// pkg/grpc/server/origin_storage_integration_test.go is the other half: it
// proves a row already on disk still resolves.
func TestJSONBFieldNamesAreFrozen(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"instance_meta key", FieldInstanceUID, "instance_uid"},
		{"destination_meta key", FieldDestinationUID, "destination_uid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("= %q, want %q. This is a storage contract, not a wire one: "+
					"renaming it orphans every instance row already written and silently "+
					"creates duplicates for guilds the bot already knows. See callermeta.go.",
					tt.got, tt.want)
			}
		})
	}
}

// The header names are a WIRE contract: server and clients ship together, so
// they are renamable — and were, to the HTTP-idiomatic hyphenated form. They
// are pinned all the same, because both ends of the encoding live in this
// package and a producer and consumer disagreeing about it was previously the
// single worst bug in this project.
func TestHeaderNamesArePinned(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"platform_enum", HeaderPlatformEnum, "ginbot-platform-enum"},
		{"user_id", HeaderUserID, "ginbot-user-id"},
		{"instance_uid", HeaderInstanceUID, "ginbot-instance-uid"},
		{"destination_uid", HeaderDestinationUID, "ginbot-destination-uid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("= %q, want %q", tt.got, tt.want)
			}
		})
	}
}

// The two groups spell related things and must never be defined in terms of
// each other. A shared constant is what would carry a header rename into the
// storage layer, which is the failure this whole arrangement exists to prevent.
func TestHeaderAndJSONBNamesAreIndependent(t *testing.T) {
	tests := []struct {
		name   string
		header string
		field  string
	}{
		{"instance", HeaderInstanceUID, FieldInstanceUID},
		{"destination", HeaderDestinationUID, FieldDestinationUID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.header == tt.field {
				t.Errorf("header and jsonb key are both %q; they are two contracts with "+
					"different lifetimes and must not be one constant", tt.header)
			}
		})
	}
}

// gRPC lowercases metadata keys on the wire, so a constant containing an
// uppercase letter is written under one name and read under another — a
// silently anonymous caller rather than an error. Cheap to assert, and the
// rename is exactly when it could have been introduced.
func TestHeaderNamesAreValidMetadataKeys(t *testing.T) {
	headers := map[string]string{
		"HeaderPlatformEnum":   HeaderPlatformEnum,
		"HeaderUserID":         HeaderUserID,
		"HeaderInstanceUID":    HeaderInstanceUID,
		"HeaderDestinationUID": HeaderDestinationUID,
	}

	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			if header != strings.ToLower(header) {
				t.Errorf("%s = %q is not lowercase; gRPC will lowercase it on the wire "+
					"and md.Get with this constant will then never match", name, header)
			}
			// -bin is gRPC's marker for base64-encoded binary values. A key
			// ending in it changes how the value is transported.
			if strings.HasSuffix(header, "-bin") {
				t.Errorf("%s = %q ends in -bin, which makes gRPC treat the value as binary", name, header)
			}

			md := metadata.Pairs(header, "value")
			if got := md.Get(header); len(got) != 1 || got[0] != "value" {
				t.Errorf("%s = %q does not round-trip through metadata.Pairs/Get: %v", name, header, got)
			}
		})
	}
}

// A client still sending the pre-rename header names must be REFUSED, not
// silently treated as having no identity.
//
// This is the same class of bug as the original break — the Discord client sent
// the enum's name while the server parsed it with strconv.ParseInt — and it is
// what a partial rollout produces: an old client against a new server. An
// InvalidArgument tells an operator what happened; an anonymous caller does not.
func TestPreRenameHeaderNamesAreNotAccepted(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"platform_enum", pb.Platform_PLATFORM_DISCORD.String(),
		"user_id", "123456789",
	))

	if _, err := FromIncomingContext(ctx); err == nil {
		t.Fatal("the old header names were accepted; the server is reading two spellings, " +
			"so a rename of either is no longer observable")
	} else if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
	}

	// And the origin half, which reports "no origin" rather than an error.
	originCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"instance_uid", "guild-1",
		"destination_uid", "channel-1",
	))
	if got, ok := OriginFromIncomingContext(originCtx); ok {
		t.Errorf("the old origin header names produced %+v; they must not be read", got)
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
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(HeaderUserID, "1")),
		},
		{
			name: "platform_enum empty",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(HeaderPlatformEnum, "")),
		},
		{
			name: "platform_enum unknown name",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(HeaderPlatformEnum, "PLATFORM_CARRIER_PIGEON")),
		},
		{
			// The pre-fix client/server mismatch: a numeric value is not a valid name.
			name: "platform_enum sent as a number",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(HeaderPlatformEnum, "1")),
		},
		{
			name: "platform_enum unspecified",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs(HeaderPlatformEnum, pb.Platform_PLATFORM_UNSPECIFIED.String())),
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
