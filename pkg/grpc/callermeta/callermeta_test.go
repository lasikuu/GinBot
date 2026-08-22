package callermeta

import (
	"context"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
