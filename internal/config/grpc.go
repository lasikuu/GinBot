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

func certsPath() string {
	return os.Getenv("GINBOT_CERTS_PATH")
}

func serverOptions() []grpc.ServerOption {
	var gRPCServerOptions []grpc.ServerOption

	if !gRPCTLS() {
		return gRPCServerOptions
	}

	tlsCredentials := auth.LoadServerCredentials()
	gRPCServerOptions = append(gRPCServerOptions, grpc.Creds(tlsCredentials))

	return gRPCServerOptions
}
