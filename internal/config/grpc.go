package config

import (
	"os"

	"github.com/lasikuu/GinBot/internal/auth"
	"google.golang.org/grpc"
)

// GRPCServerOptions is the model for gRPC configuration.
type GRPCServerOptions struct {
	Host          string
	Port          string
	TLS           bool
	CertsPath     string
	ServerOptions []grpc.ServerOption
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

func serverOptions() []grpc.ServerOption {
	var gRPCServerOptions []grpc.ServerOption

	if !gRPCTLS() {
		return gRPCServerOptions
	}

	tlsCredentials := auth.LoadServerCredentials(certsPath())
	gRPCServerOptions = append(gRPCServerOptions, grpc.Creds(tlsCredentials))

	return gRPCServerOptions
}
