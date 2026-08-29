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
)

// DefaultCertsDir is relative to the working directory.
const DefaultCertsDir = "cert"

// AppendCertsFromPEM only returns false, so an empty pool needs a sentinel.
var errNoCACertificates = errors.New("no ca certificates found in pem")

// Parameter order is key before cert, the reverse of tls.LoadX509KeyPair.
func loadCredentials(certsDir, caCertPEM, keyPEM, certPEM string) (tls.Certificate, *x509.CertPool, error) {
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

// NextProtos is left unset: ServeTLS appends "h2" itself from srv.Protocols.
func ServerTLSConfig(certsDir string) (*tls.Config, error) {
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

// ServerName is left unset, so the peer is verified against the dialled host.
func ClientTLSConfig(certsDir string) (*tls.Config, error) {
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

// Fatal on failure: starting without the configured mTLS is a silent downgrade.
func LoadServerTLSConfig(certsDir string) *tls.Config {
	conf, err := ServerTLSConfig(certsDir)
	if err != nil {
		log.Z.Fatal("failed to load server TLS configuration", zap.Error(err))
	}

	return conf
}
