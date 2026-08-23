package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
)

// DefaultCertsDir is used when GINBOT_CERTS_PATH is unset. It is relative to the
// working directory, so with the default the process must be launched from the repo root.
const DefaultCertsDir = "cert"

// errNoCACertificates reports a CA file that parsed as a file but yielded no
// usable certificate. x509.CertPool.AppendCertsFromPEM only reports this as a
// false return with no error of its own, so the condition needs a sentinel of
// its own to be distinguishable from a read failure.
var errNoCACertificates = errors.New("no ca certificates found in pem")

// loadCredentials reads the CA pool and the key pair out of certsDir.
//
// The parameter order is (certsDir, caCertPEM, keyPEM, certPEM) — KEY BEFORE
// CERT, which is the reverse of how tls.LoadX509KeyPair takes them. That is
// deliberate and both call sites depend on it; do not silently reorder it.
func loadCredentials(certsDir, caCertPEM, keyPEM, certPEM string) (tls.Certificate, *x509.CertPool, error) {
	// config.certsPath() already substitutes DefaultCertsDir, but this package is
	// callable directly, so guard here too rather than joining onto "".
	if certsDir == "" {
		certsDir = DefaultCertsDir
	}

	caPath := filepath.Join(certsDir, caCertPEM)
	caPem, err := os.ReadFile(caPath)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read ca pem %s: %w", caPath, err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPem) {
		return tls.Certificate{}, nil, fmt.Errorf("append ca cert %s: %w", caPath, errNoCACertificates)
	}

	certPath := filepath.Join(certsDir, certPEM)
	keyPath := filepath.Join(certsDir, keyPEM)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load key pair cert %s key %s: %w", certPath, keyPath, err)
	}

	return cert, certPool, nil
}

// serverTLSConfig builds the server's mutual TLS configuration from certsDir.
func serverTLSConfig(certsDir string) (*tls.Config, error) {
	tlsCert, certPool, err := loadCredentials(certsDir, "ca-cert.pem", "server-key.pem", "server-cert.pem")
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// clientTLSConfig builds a client's mutual TLS configuration from certsDir.
func clientTLSConfig(certsDir string) (*tls.Config, error) {
	tlsCert, certPool, err := loadCredentials(certsDir, "ca-cert.pem", "client-key.pem", "client-cert.pem")
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		RootCAs:      certPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// LoadServerCredentials loads the server's mutual TLS credentials from certsDir.
//
// It is fatal on failure, on purpose: this runs during startup wiring, and a
// server that came up without the mutual TLS it was configured for would be a
// silent downgrade. The error-returning half lives in serverTLSConfig so the
// loading itself stays exercisable.
func LoadServerCredentials(certsDir string) credentials.TransportCredentials {
	conf, err := serverTLSConfig(certsDir)
	if err != nil {
		log.Z.Fatal("failed to load server credentials", zap.Error(err))
	}

	return credentials.NewTLS(conf)
}

// LoadClientCredentials loads the client's mutual TLS credentials from certsDir.
// Fatal on failure, for the same reason as LoadServerCredentials.
func LoadClientCredentials(certsDir string) credentials.TransportCredentials {
	conf, err := clientTLSConfig(certsDir)
	if err != nil {
		log.Z.Fatal("failed to load client credentials", zap.Error(err))
	}

	return credentials.NewTLS(conf)
}
