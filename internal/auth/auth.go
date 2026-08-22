package auth

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
)

// DefaultCertsDir is used when GINBOT_CERTS_PATH is unset. It is relative to the
// working directory, so with the default the process must be launched from the repo root.
const DefaultCertsDir = "cert"

func loadCredentials(certsDir string, caCertPEM string, keyPEM string, certPEM string) (tls.Certificate, *x509.CertPool) {
	if certsDir == "" {
		certsDir = DefaultCertsDir
	}

	caPath := filepath.Join(certsDir, caCertPEM)
	caPem, err := os.ReadFile(caPath)
	if err != nil {
		log.Z.Fatal("failed to read ca pem", zap.String("path", caPath), zap.Error(err))
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPem) {
		log.Z.Fatal("failed to append ca cert", zap.String("path", caPath))
	}

	certPath := filepath.Join(certsDir, certPEM)
	keyPath := filepath.Join(certsDir, keyPEM)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		log.Z.Fatal("failed to load key pair",
			zap.String("cert", certPath), zap.String("key", keyPath), zap.Error(err))
	}

	return cert, certPool
}

// LoadServerCredentials loads the server's mutual TLS credentials from certsDir.
func LoadServerCredentials(certsDir string) credentials.TransportCredentials {
	tlsCert, certPool := loadCredentials(certsDir, "ca-cert.pem", "server-key.pem", "server-cert.pem")

	conf := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
		MinVersion:   tls.VersionTLS13,
	}

	return credentials.NewTLS(conf)
}

// LoadClientCredentials loads the client's mutual TLS credentials from certsDir.
func LoadClientCredentials(certsDir string) credentials.TransportCredentials {
	tlsCert, certPool := loadCredentials(certsDir, "ca-cert.pem", "client-key.pem", "client-cert.pem")

	conf := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		RootCAs:      certPool,
		MinVersion:   tls.VersionTLS13,
	}

	return credentials.NewTLS(conf)
}
