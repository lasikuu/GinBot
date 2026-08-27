package config

import (
	"os"

	"github.com/lasikuu/GinBot/internal/auth"
)

// GRPCServerOptions is the model for the Connect boundary configuration.
//
// The name predates the Connect port and is kept: the wire protocol changed,
// the environment variables and the shape of what every binary needs to know
// (where the boundary is, and whether it is TLS) did not. It is read by both
// sides of that boundary — cmd/ginbot-server binds Host:Port, and the platform
// clients dial the same address — which is why it lives on OptionsModel rather
// than being folded into a server-only struct.
//
// It deliberately holds no built transport of any kind: building an
// http2.Transport or an http.Server loads the TLS key pair, and SetEnv runs in
// all three binaries. With GINBOT_GRPC_TLS=true that would make the Discord and
// Matrix clients fatal unless the server's own certificate were also on their
// host. Host, Port, TLS and CertsPath are plain data; each binary decides for
// itself what to build from them — cmd/ginbot-server via auth.ServerTLSConfig,
// the platform clients (stage 4) via auth.ClientTLSConfig, both fed by the
// CertsPath here.
type GRPCServerOptions struct {
	Host      string
	Port      string
	TLS       bool
	CertsPath string
}

// MaxGRPCMessageBytes bounds a single Connect message. It must exceed
// storage.MaxFileBytes, because GetFile returns a file's bytes inline in one
// unary response rather than as a stream.
//
// That used to be the whole reason this lived at the RPC layer rather than
// only in pkg/storage: a streamed GetFile would have run outside the
// unary-only clearance interceptor and therefore outside authorization. That
// specific reason is gone — pkg/grpc/interceptor.ClearanceInterceptor now
// covers WrapStreamingHandler too — but the cap is not: an unbounded message is
// still a resource-exhaustion vector independent of authorization, and
// GetFile's unary shape is still what it is. cmd/ginbot-server applies this
// limit to TriggerService alone, via connect.WithReadMaxBytes /
// WithSendMaxBytes on that service's handler options; every other service
// keeps the Connect default. A test asserts the relationship to
// storage.MaxFileBytes; that relationship, not the streaming argument, is why
// the constant stays exactly where it is.
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
