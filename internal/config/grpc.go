package config

import (
	"os"

	"google.golang.org/grpc"
)

// GRPCOptions is the model for gRPC configuration.
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
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(1024 * 1024 * 1024),
		grpc.MaxSendMsgSize(1024 * 1024 * 1024),
		grpc.MaxConcurrentStreams(100),
	}
}
