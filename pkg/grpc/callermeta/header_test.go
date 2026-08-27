package callermeta

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/grpc/metadata"
)

// metadataFromHeaderForTest builds the incoming gRPC context
// FromIncomingContext expects, from the same http.Header values FromHeader
// was given, so TestFromHeaderAndFromIncomingContextAgree drives both parsers
// off one shared fixture rather than two that could quietly diverge.
func metadataFromHeaderForTest(header http.Header) context.Context {
	pairs := make([]string, 0, len(header)*2)
	for key, values := range header {
		for _, value := range values {
			pairs = append(pairs, key, value)
		}
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(pairs...))
}

// FromHeader and OriginFromHeader are the Connect (http.Header) counterparts
// of FromIncomingContext and OriginFromIncomingContext, added for stage 3:
// a Connect handler only ever sees headers on the request or connection
// object, never folded into ctx the way gRPC metadata was. TestRoundTrip and
// friends in callermeta_test.go already pin the gRPC-metadata half; this file
// is the same set of properties pinned against the header-based API instead,
// so a producer/consumer disagreement on THIS path is caught the same way the
// original enum-name-vs-number bug would have been.

// TestFromHeaderRoundTrip is the header-based counterpart of TestRoundTrip.
func TestFromHeaderRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		platform pb.Platform
		uid      string
		wantUID  bool
	}{
		{"discord with user", pb.Platform_PLATFORM_DISCORD, "123456789", true},
		{"matrix with user", pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org", true},
		// No user id: the caller is acting on its own behalf, which is normal
		// for cron-driven and health traffic. The header must simply be
		// absent, not present and empty.
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

			// No origin header was attached, and none must be reported.
			if got, ok := OriginFromHeader(header); ok {
				t.Errorf("origin = %+v on a call that carried none", got)
			}
		})
	}
}

// TestFromHeaderErrors is the header-based counterpart of
// TestFromIncomingContextErrors: every malformed shape must produce
// connect.CodeInvalidArgument, not an anonymous caller.
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
			// The pre-callermeta client/server mismatch: a numeric value is
			// not a valid name.
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

// TestOriginFromHeaderRoundTrip is the header-based counterpart of
// TestOriginRoundTrip.
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
			// A direct message belongs to no guild, so a client would never
			// send an instance header at all.
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

// TestFromHeaderAndFromIncomingContextAgree pins the promise in FromHeader's
// own doc comment: "a caller cannot tell which transport parsed its request
// from the response alone." Both parsers are fed byte-identical header values
// and must produce the same verdict.
func TestFromHeaderAndFromIncomingContextAgree(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		uid         string
		setPlatform bool
		setUID      bool
	}{
		{"well formed", pb.Platform_PLATFORM_DISCORD.String(), "123", true, true},
		{"no uid", pb.Platform_PLATFORM_DISCORD.String(), "", true, false},
		{"missing platform", "", "123", false, true},
		{"unspecified platform", pb.Platform_PLATFORM_UNSPECIFIED.String(), "123", true, true},
		{"unknown platform name", "PLATFORM_CARRIER_PIGEON", "123", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make(http.Header)
			if tt.setPlatform {
				header.Set(HeaderPlatformEnum, tt.platform)
			}
			if tt.setUID {
				header.Set(HeaderUserID, tt.uid)
			}

			headerCaller, headerErr := FromHeader(header)

			ctx := metadataFromHeaderForTest(header)
			ctxCaller, ctxErr := FromIncomingContext(ctx)

			if (headerErr == nil) != (ctxErr == nil) {
				t.Fatalf("FromHeader err = %v, FromIncomingContext err = %v; the two transports disagree", headerErr, ctxErr)
			}
			if headerErr != nil {
				return
			}
			if headerCaller.PlatformEnum != ctxCaller.PlatformEnum {
				t.Errorf("platform: header = %v, context = %v", headerCaller.PlatformEnum, ctxCaller.PlatformEnum)
			}
			headerHasUID := headerCaller.PlatformUID != nil
			ctxHasUID := ctxCaller.PlatformUID != nil
			if headerHasUID != ctxHasUID {
				t.Fatalf("PlatformUID presence: header = %v, context = %v", headerHasUID, ctxHasUID)
			}
			if headerHasUID && *headerCaller.PlatformUID != *ctxCaller.PlatformUID {
				t.Errorf("PlatformUID: header = %q, context = %q", *headerCaller.PlatformUID, *ctxCaller.PlatformUID)
			}
		})
	}
}
