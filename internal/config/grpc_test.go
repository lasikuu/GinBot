package config

import (
	"testing"

	"github.com/lasikuu/GinBot/internal/auth"
)

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

// Unlike dbPort(), never parsed: the value goes straight to net.Listen.
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

// GINBOT_GRPC_TLS=1 leaves the server plaintext.
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
