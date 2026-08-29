package config

import (
	"net"
	"os"

	"github.com/lasikuu/GinBot/internal/auth"
)

// GRPCServerOptions configures the Connect boundary. Host is a bind address
// for the server and a dial address for the clients.
type GRPCServerOptions struct {
	Host      string
	Port      string
	TLS       bool
	CertsPath string
}

// ClientBaseURL is the scheme://host:port the platform clients dial.
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

// A relative certsPath is resolved against the working directory.
func certsPath() string {
	value := os.Getenv("GINBOT_CERTS_PATH")
	if value == "" {
		return auth.DefaultCertsDir
	}
	return value
}
