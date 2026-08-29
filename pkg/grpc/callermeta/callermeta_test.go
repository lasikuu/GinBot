package callermeta

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// headerFor materialises ctx into an http.Header, so tests pin the wire form
// rather than how values are stashed on the context.
func headerFor(ctx context.Context) http.Header {
	header := make(http.Header)
	WriteHeader(ctx, header)
	return header
}

// The wire form is asserted alongside the decoded result: a pure round trip
// would still pass if both ends had moved to a third spelling together.
func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		platform pb.Platform
		uid      string
		wantUID  bool
	}{
		{"discord with user", pb.Platform_PLATFORM_DISCORD, "123456789", true},
		{"matrix with user", pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org", true},
		// No user id: the header must be absent, not present and empty.
		{"discord without user", pb.Platform_PLATFORM_DISCORD, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewOutgoingContext(context.Background(), tt.platform, tt.uid)
			header := headerFor(ctx)

			if got := header.Get(HeaderPlatformEnum); got != tt.platform.String() {
				t.Errorf("outgoing %q = %q, want %q", HeaderPlatformEnum, got, tt.platform.String())
			}
			if tt.wantUID {
				if got := header.Get(HeaderUserID); got != tt.uid {
					t.Errorf("outgoing %q = %q, want %q", HeaderUserID, got, tt.uid)
				}
			} else if got := header.Get(HeaderUserID); got != "" {
				t.Errorf("outgoing %q = %q, want it absent entirely", HeaderUserID, got)
			}

			if got, ok := OriginFromHeader(header); ok {
				t.Errorf("origin = %+v on a call that carried none", got)
			}

			caller, err := FromHeader(header)
			if err != nil {
				t.Fatalf("FromHeader: %v", err)
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
			header := headerFor(ctx)

			if got := header.Get(HeaderInstanceUID); tt.wantOK {
				if got != tt.wantInstance {
					t.Errorf("outgoing %q = %q, want %q", HeaderInstanceUID, got, tt.wantInstance)
				}
			} else if got != "" {
				t.Errorf("outgoing %q = %q, want it absent entirely", HeaderInstanceUID, got)
			}

			got, ok := OriginFromHeader(header)
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

func TestOriginAndIdentityCoexist(t *testing.T) {
	ctx := NewOutgoingContext(context.Background(), pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org")
	ctx = NewOutgoingOrigin(ctx, Origin{InstanceUID: "!room:example.org", DestinationUID: "!room:example.org"})

	header := headerFor(ctx)

	caller, err := FromHeader(header)
	if err != nil {
		t.Fatalf("FromHeader: %v", err)
	}
	if caller.PlatformEnum != pb.Platform_PLATFORM_MATRIX_PROTOCOL {
		t.Errorf("platform = %v, want %v", caller.PlatformEnum, pb.Platform_PLATFORM_MATRIX_PROTOCOL)
	}
	if caller.PlatformUID == nil || *caller.PlatformUID != "@a:example.org" {
		t.Errorf("PlatformUID = %v, want @a:example.org", caller.PlatformUID)
	}

	if _, ok := OriginFromHeader(header); !ok {
		t.Error("origin was lost when identity was attached")
	}
}

func TestOriginOrderDoesNotMatter(t *testing.T) {
	origin := Origin{InstanceUID: "guild-1", DestinationUID: "channel-1"}

	wrongOrderFirst := NewOutgoingOrigin(context.Background(), origin)
	wrongOrderFirst = NewOutgoingContext(wrongOrderFirst, pb.Platform_PLATFORM_DISCORD, "uid")

	rightOrderFirst := NewOutgoingContext(context.Background(), pb.Platform_PLATFORM_DISCORD, "uid")
	rightOrderFirst = NewOutgoingOrigin(rightOrderFirst, origin)

	headerA := headerFor(wrongOrderFirst)
	headerB := headerFor(rightOrderFirst)

	for _, name := range []string{HeaderPlatformEnum, HeaderUserID, HeaderInstanceUID, HeaderDestinationUID} {
		if a, b := headerA.Get(name), headerB.Get(name); a != b {
			t.Errorf("%s: origin-first order produced %q, identity-first order produced %q; the two orders must agree", name, a, b)
		}
	}

	// Both orders must carry the origin, not merely agree on dropping it.
	if _, ok := OriginFromHeader(headerA); !ok {
		t.Error("origin-first order lost the origin; the replacement hazard is back")
	}
	if _, ok := OriginFromHeader(headerB); !ok {
		t.Error("identity-first order lost the origin")
	}
}

// The jsonb shapes are matched by equality against rows already written.
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

// The jsonb field names are a storage contract: renaming one does not fail, it
// silently stops matching rows already written.
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

// A shared constant would carry a header rename into the storage layer.
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

func TestHeaderNamesAreValidHTTPHeaderKeys(t *testing.T) {
	headers := map[string]string{
		"HeaderPlatformEnum":   HeaderPlatformEnum,
		"HeaderUserID":         HeaderUserID,
		"HeaderInstanceUID":    HeaderInstanceUID,
		"HeaderDestinationUID": HeaderDestinationUID,
	}

	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			if header != strings.ToLower(header) {
				t.Errorf("%s = %q is not lowercase", name, header)
			}

			h := make(http.Header)
			h.Set(header, "value")
			if got := h.Get(header); got != "value" {
				t.Errorf("%s = %q does not round-trip through http.Header.Set/Get: %v", name, header, h)
			}
		})
	}
}

// An old client against a new server must be refused, not treated as anonymous.
func TestPreRenameHeaderNamesAreNotAccepted(t *testing.T) {
	header := make(http.Header)
	header.Set("platform_enum", pb.Platform_PLATFORM_DISCORD.String())
	header.Set("user_id", "123456789")

	if _, err := FromHeader(header); err == nil {
		t.Fatal("the old header names were accepted; the server is reading two spellings, " +
			"so a rename of either is no longer observable")
	} else if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
	}

	// The origin half reports "no origin" rather than an error.
	originHeader := make(http.Header)
	originHeader.Set("instance_uid", "guild-1")
	originHeader.Set("destination_uid", "channel-1")
	if got, ok := OriginFromHeader(originHeader); ok {
		t.Errorf("the old origin header names produced %+v; they must not be read", got)
	}
}
