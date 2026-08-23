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

// MaxGRPCMessageBytes bounds a single gRPC message. It must exceed
// storage.MaxFileBytes, because GetFile returns a file's bytes inline in one
// unary response rather than as a stream: streaming would put the RPC outside
// the unary clearance interceptor and therefore outside authorization.
//
// Not imported from pkg/storage to avoid pulling a storage dependency into
// every gRPC client (Discord, Matrix) for the sake of one constant; the
// relationship between the two values is enforced only by this comment, not
// by the compiler.
const MaxGRPCMessageBytes = 12 << 20

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
	gRPCServerOptions := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxGRPCMessageBytes),
		grpc.MaxSendMsgSize(MaxGRPCMessageBytes),
	}

	if !gRPCTLS() {
		return gRPCServerOptions
	}

	tlsCredentials := auth.LoadServerCredentials(certsPath())
	gRPCServerOptions = append(gRPCServerOptions, grpc.Creds(tlsCredentials))

	return gRPCServerOptions
}
