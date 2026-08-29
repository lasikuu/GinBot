package callermeta

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// TestFromHeaderRoundTrip exercises FromHeader against hand-built headers,
// independent of the encode side.
func TestFromHeaderRoundTrip(t *testing.T) {
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
			header := make(http.Header)
			header.Set(HeaderPlatformEnum, tt.platform.String())
			if tt.uid != "" {
				header.Set(HeaderUserID, tt.uid)
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

			if got, ok := OriginFromHeader(header); ok {
				t.Errorf("origin = %+v on a call that carried none", got)
			}
		})
	}
}

// Every malformed shape must be InvalidArgument, not an anonymous caller.
func TestFromHeaderErrors(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
	}{
		{"no header at all", make(http.Header)},
		{
			name: "platform_enum missing",
			header: func() http.Header {
				h := make(http.Header)
				h.Set(HeaderUserID, "1")
				return h
			}(),
		},
		{
			name: "platform_enum empty",
			header: func() http.Header {
				h := make(http.Header)
				h.Set(HeaderPlatformEnum, "")
				return h
			}(),
		},
		{
			name: "platform_enum unknown name",
			header: func() http.Header {
				h := make(http.Header)
				h.Set(HeaderPlatformEnum, "PLATFORM_CARRIER_PIGEON")
				return h
			}(),
		},
		{
			name: "platform_enum sent as a number",
			header: func() http.Header {
				h := make(http.Header)
				h.Set(HeaderPlatformEnum, "1")
				return h
			}(),
		},
		{
			name: "platform_enum unspecified",
			header: func() http.Header {
				h := make(http.Header)
				h.Set(HeaderPlatformEnum, pb.Platform_PLATFORM_UNSPECIFIED.String())
				return h
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromHeader(tt.header)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
				t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestOriginFromHeaderRoundTrip(t *testing.T) {
	tests := []struct {
		name            string
		instanceUID     string
		destinationUID  string
		setInstance     bool
		setDestination  bool
		wantOK          bool
		wantInstance    string
		wantDestination string
	}{
		{
			name: "guild and channel", setInstance: true, setDestination: true,
			instanceUID: "guild-1", destinationUID: "channel-1",
			wantOK: true, wantInstance: "guild-1", wantDestination: "channel-1",
		},
		{
			name: "instance only", setInstance: true,
			instanceUID: "guild-1",
			wantOK:      true, wantInstance: "guild-1",
		},
		{
			name:   "no instance header at all",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make(http.Header)
			if tt.setInstance {
				header.Set(HeaderInstanceUID, tt.instanceUID)
			}
			if tt.setDestination {
				header.Set(HeaderDestinationUID, tt.destinationUID)
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

func TestEncodeThenDecodeAgreesWithWhatWasAttached(t *testing.T) {
	tests := []struct {
		name           string
		attachIdentity bool
		platform       pb.Platform
		uid            string
		wantErr        bool
	}{
		{name: "well formed", attachIdentity: true, platform: pb.Platform_PLATFORM_DISCORD, uid: "123"},
		{name: "no uid", attachIdentity: true, platform: pb.Platform_PLATFORM_DISCORD},
		{
			// The platform is a required typed parameter, so the only way to
			// reach "missing platform" from the encode side is no identity.
			name:    "identity never attached",
			wantErr: true,
		},
		{
			name:           "unspecified platform",
			attachIdentity: true,
			platform:       pb.Platform_PLATFORM_UNSPECIFIED,
			uid:            "123",
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.attachIdentity {
				ctx = NewOutgoingContext(ctx, tt.platform, tt.uid)
			}
			ctx = NewOutgoingOrigin(ctx, Origin{InstanceUID: "guild-1", DestinationUID: "channel-1"})

			header := make(http.Header)
			WriteHeader(ctx, header)

			caller, err := FromHeader(header)
			if tt.wantErr {
				if err == nil {
					t.Fatal("FromHeader succeeded, want an error")
				}
				if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
					t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromHeader: %v", err)
			}
			if caller.PlatformEnum != tt.platform {
				t.Errorf("platform: decoded %v, attached %v", caller.PlatformEnum, tt.platform)
			}
			hasUID := caller.PlatformUID != nil
			wantUID := tt.uid != ""
			if hasUID != wantUID {
				t.Fatalf("PlatformUID presence: decoded %v, attached uid %q", hasUID, tt.uid)
			}
			if hasUID && *caller.PlatformUID != tt.uid {
				t.Errorf("PlatformUID: decoded %q, attached %q", *caller.PlatformUID, tt.uid)
			}

			// The origin is always attached here, whatever happened to identity.
			origin, ok := OriginFromHeader(header)
			if !ok {
				t.Fatal("origin was attached but did not decode")
			}
			if origin.InstanceUID != "guild-1" || origin.DestinationUID != "channel-1" {
				t.Errorf("origin = %+v, want {guild-1 channel-1}", origin)
			}
		})
	}
}
