package auth

import (
	"crypto/tls"
	"crypto/x509"
	"os"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
)

func loadCredentials(caCertPEM string, keyPEM string, certPEM string) (tls.Certificate, *x509.CertPool) {
	caPem, err := os.ReadFile(caCertPEM)
	if err != nil {
		log.Z.Fatal("failed to read ca pem", zap.Error(err))
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPem) {
		log.Z.Fatal("failed to append ca-cert.pem")
	}

	serverCert, err := tls.LoadX509KeyPair(certPEM, keyPEM)
	if err != nil {
		log.Z.Fatal("failed to load server-cert.pem and server-key.pem", zap.Error(err))
	}

	return serverCert, certPool
}

func LoadServerCredentials() credentials.TransportCredentials {
	tlsCert, certPool := loadCredentials("cert/ca-cert.pem", "cert/server-key.pem", "cert/server-cert.pem")

	conf := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
	}

	return credentials.NewTLS(conf)
}

func LoadClientCredentials() credentials.TransportCredentials {
	tlsCert, certPool := loadCredentials("cert/ca-cert.pem", "cert/client-key.pem", "cert/client-cert.pem")

	// set config of tls credential
	config := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		RootCAs:      certPool,
	}

	return credentials.NewTLS(config)
}
