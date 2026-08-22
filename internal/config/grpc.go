package config

import (
	"os"

	"github.com/lasikuu/GinBot/internal/auth"
	"google.golang.org/grpc"
)

// GRPCServerOptions is the model for gRPC configuration.
//
// It deliberately holds no []grpc.ServerOption: building those loads the
// *server* key pair, and SetEnv runs in all three binaries. With
// GINBOT_GRPC_TLS=true that made the Discord and Matrix clients fatal unless
// server-cert.pem and server-key.pem were also present on their host. The
// server binary calls ServerOptions() for itself instead.
type GRPCServerOptions struct {
	Host      string
	Port      string
	TLS       bool
	CertsPath string
}

type GRPCClientOptions struct {
	DialOptions []grpc.DialOption
}

func gRPCHost() string {
	value := os.Getenv("GINBOT_GRPC_HOST")
	if value == "" {
		return "localhost"
	}
	return value
}

func gRPCPort() string {
	value := os.Getenv("GINBOT_GRPC_PORT")
	if value == "" {
		return "50051"
	}
	return value
}

func gRPCTLS() bool {
	return os.Getenv("GINBOT_GRPC_TLS") == "true"
}

// certsPath returns the directory holding the mutual TLS material.
// Relative paths are resolved against the working directory.
func certsPath() string {
	value := os.Getenv("GINBOT_CERTS_PATH")
	if value == "" {
		return auth.DefaultCertsDir
	}
	return value
}

// ServerOptions builds the gRPC server options, loading the server key pair when
// TLS is enabled. Only the server binary should call this — see the note on
// GRPCServerOptions.
func ServerOptions() []grpc.ServerOption {
	var gRPCServerOptions []grpc.ServerOption

	if !gRPCTLS() {
		return gRPCServerOptions
	}

	tlsCredentials := auth.LoadServerCredentials(certsPath())
	gRPCServerOptions = append(gRPCServerOptions, grpc.Creds(tlsCredentials))

	return gRPCServerOptions
}
