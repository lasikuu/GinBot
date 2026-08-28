package config

import (
	"net"
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

// ClientBaseURL is the scheme://host:port the platform clients dial.
//
// Plain data, deliberately: it names the boundary without building anything
// transport-shaped from it, matching the reasoning on GRPCServerOptions
// above. ClientOptions in client.go is what turns this into a client.Options,
// including loading TLS material when TLS is set.
func (o GRPCServerOptions) ClientBaseURL() string {
	scheme := "http"
	if o.TLS {
		scheme = "https"
	}

	return scheme + "://" + net.JoinHostPort(o.Host, o.Port)
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
