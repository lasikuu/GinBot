package config

import (
	"testing"

	"github.com/lasikuu/GinBot/internal/auth"
)

// Tests for internal/config/grpc.go. TestMain and unsetEnv come from
// repost_test.go.
//
// ServerOptions() and dialOptions() are gone: stage 3 deletes both, along with
// GRPCClientOptions, because internal/config stops constructing anything
// transport-shaped at all — that is what kills the M4 hazard (the server
// binary requiring a client certificate) structurally rather than by
// convention. What is left to test here is the plain data: gRPCHost,
// gRPCPort, gRPCTLS and certsPath. TLS credential loading itself is covered
// in internal/auth/auth_test.go against generated fixtures.
//
// MaxGRPCMessageBytes is gone too, this stage: GetFile stopped returning a
// file's bytes inline in one unary response (it streams
// GetFileChunkBytes-sized chunks instead, pkg/grpc/server/trigger.go), which
// was the entire reason this constant existed above storage.MaxFileBytes.
// internal/clientopts now carries its own local message-size constant instead
// — see its own doc comment — so there is nothing left here to guard the
// relationship TestMaxGRPCMessageBytesExceedsTheFileSizeCap used to pin. That
// test function is REMOVED, not weakened: the guarantee it protected (a
// message cap that cannot make the largest storable file unreadable) is now
// structurally true regardless of the constant's value, because no single
// GetFile message ever carries more than one chunk.

func TestGRPCHost(t *testing.T) {
	unsetEnv(t, "GINBOT_GRPC_HOST")
	if got := gRPCHost(); got != "localhost" {
		t.Errorf("gRPCHost() = %q, want the default %q", got, "localhost")
	}

	t.Setenv("GINBOT_GRPC_HOST", "")
	if got := gRPCHost(); got != "localhost" {
		t.Errorf("gRPCHost() = %q for an empty value, want the default %q", got, "localhost")
	}

	t.Setenv("GINBOT_GRPC_HOST", "ginbot.internal")
	if got := gRPCHost(); got != "ginbot.internal" {
		t.Errorf("gRPCHost() = %q, want %q", got, "ginbot.internal")
	}
}

// TestGRPCPortIsKeptAsAStringAndNeverParsed: the port is joined into a listen
// address, never converted to a number, so a value the accessor cannot
// validate is passed straight through to net.Listen rather than being
// silently replaced by the default. Pinned because the neighbouring dbPort()
// DOES parse and fall back, and the asymmetry is easy to "fix" by mistake.
func TestGRPCPortIsKeptAsAStringAndNeverParsed(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  string
	}{
		{"unset defaults to 50051", false, "", "50051"},
		{"an empty value defaults to 50051", true, "", "50051"},
		{"a numeric port is passed through", true, "9090", "9090"},
		{"a non-numeric value is passed through unchanged", true, "not-a-port", "not-a-port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("GINBOT_GRPC_PORT", tt.value)
			} else {
				unsetEnv(t, "GINBOT_GRPC_PORT")
			}

			if got := gRPCPort(); got != tt.want {
				t.Errorf("gRPCPort() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGRPCTLSRequiresTheExactLiteralTrue: this switch decides whether the
// transport is mutually authenticated or plaintext, so an operator who writes
// GINBOT_GRPC_TLS=1 and believes they have enabled TLS is running the server
// wide open. The exact-match rule is asserted, not assumed.
func TestGRPCTLSRequiresTheExactLiteralTrue(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{"unset means plaintext", false, "", false},
		{"the exact lowercase true enables it", true, "true", true},
		{"uppercase TRUE does NOT enable it", true, "TRUE", false},
		{"mixed-case True does NOT enable it", true, "True", false},
		{"one does NOT enable it", true, "1", false},
		{"yes does NOT enable it", true, "yes", false},
		{"false means plaintext", true, "false", false},
		{"an empty value means plaintext", true, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("GINBOT_GRPC_TLS", tt.value)
			} else {
				unsetEnv(t, "GINBOT_GRPC_TLS")
			}

			if got := gRPCTLS(); got != tt.want {
				t.Errorf("gRPCTLS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCertsPath(t *testing.T) {
	unsetEnv(t, "GINBOT_CERTS_PATH")
	if got := certsPath(); got != auth.DefaultCertsDir {
		t.Errorf("certsPath() = %q, want auth.DefaultCertsDir (%q)", got, auth.DefaultCertsDir)
	}

	t.Setenv("GINBOT_CERTS_PATH", "")
	if got := certsPath(); got != auth.DefaultCertsDir {
		t.Errorf("certsPath() = %q for an empty value, want auth.DefaultCertsDir (%q)", got, auth.DefaultCertsDir)
	}

	t.Setenv("GINBOT_CERTS_PATH", "/etc/ginbot/certs")
	if got := certsPath(); got != "/etc/ginbot/certs" {
		t.Errorf("certsPath() = %q, want %q", got, "/etc/ginbot/certs")
	}
}
