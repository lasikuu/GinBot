package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// log.Z is nil until something initialises it, and the fatal loader uses it.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	os.Exit(m.Run())
}

// Certificate material is minted here: cert/*.pem is gitignored.

// The exact names loadCredentials joins onto certsDir, so a rename breaks
// these tests instead of hiding every fixture.
const (
	caFile         = "ca-cert.pem"
	serverCertFile = "server-cert.pem"
	serverKeyFile  = "server-key.pem"
	clientCertFile = "client-cert.pem"
	clientKeyFile  = "client-key.pem"
)

type authority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func newAuthority(t *testing.T) *authority {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ginbot-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("self-sign CA: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}

	return &authority{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

func (a *authority) issue(t *testing.T, commonName string) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate %s key: %v", commonName, err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatalf("sign %s: %v", commonName, err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal %s key: %v", commonName, err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func writeFile(t *testing.T, dir, name string, content []byte) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// validCertsDir uses the file names the production code expects.
func validCertsDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	ca := newAuthority(t)

	serverCert, serverKey := ca.issue(t, "ginbot-server")
	clientCert, clientKey := ca.issue(t, "ginbot-client")

	writeFile(t, dir, caFile, ca.certPEM)
	writeFile(t, dir, serverCertFile, serverCert)
	writeFile(t, dir, serverKeyFile, serverKey)
	writeFile(t, dir, clientCertFile, clientCert)
	writeFile(t, dir, clientKeyFile, clientKey)

	return dir
}

// Dropping MinVersion silently permits TLS 1.0; dropping ClientAuth silently
// accepts any client.
func TestServerTLSConfigRequiresTLS13AndClientCertificates(t *testing.T) {
	dir := validCertsDir(t)

	conf, err := ServerTLSConfig(dir)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}

	if len(conf.Certificates) != 1 {
		t.Fatalf("Certificates = %d, want exactly 1 (the server key pair)", len(conf.Certificates))
	}
	if conf.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want tls.RequireAndVerifyClientCert; anything weaker accepts an unauthenticated client",
			conf.ClientAuth)
	}
	if conf.ClientCAs == nil {
		t.Error("ClientCAs is nil; with RequireAndVerifyClientCert and no pool there is nothing to verify against")
	}
	if conf.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want tls.VersionTLS13 (%#x)", conf.MinVersion, tls.VersionTLS13)
	}
}

// InsecureSkipVerify turns mutual TLS into an unauthenticated tunnel.
func TestClientTLSConfigRequiresTLS13AndVerifiesTheServer(t *testing.T) {
	dir := validCertsDir(t)

	conf, err := ClientTLSConfig(dir)
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}

	if len(conf.Certificates) != 1 {
		t.Fatalf("Certificates = %d, want exactly 1 (the client key pair)", len(conf.Certificates))
	}
	if conf.RootCAs == nil {
		t.Error("RootCAs is nil; the client would fall back to the system pool and trust a public CA instead of ours")
	}
	if conf.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want tls.VersionTLS13 (%#x)", conf.MinVersion, tls.VersionTLS13)
	}
	if conf.InsecureSkipVerify {
		t.Error("InsecureSkipVerify is set; the server's certificate would not be checked at all")
	}
}

func TestLoadCredentialsReturnsTheKeyPairAndTheCAPool(t *testing.T) {
	dir := validCertsDir(t)

	cert, pool, err := loadCredentials(dir, caFile, serverKeyFile, serverCertFile)
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Error("returned tls.Certificate carries no DER chain")
	}
	if pool == nil {
		t.Fatal("returned certificate pool is nil")
	}
	if len(pool.Subjects()) == 0 { //nolint:staticcheck // Subjects is the only way to observe a non-empty pool
		t.Error("certificate pool is empty; AppendCertsFromPEM added nothing")
	}
}

// Valid fixtures only: the loader's failure paths end at log.Z.Fatal.
func TestLoadServerTLSConfigReturnsAUsableConfig(t *testing.T) {
	dir := validCertsDir(t)

	got := LoadServerTLSConfig(dir)
	if got == nil {
		t.Fatal("LoadServerTLSConfig returned nil for a valid certificate directory")
	}
	if len(got.Certificates) != 1 {
		t.Errorf("Certificates = %d, want exactly 1", len(got.Certificates))
	}
	if got.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want tls.RequireAndVerifyClientCert", got.ClientAuth)
	}
	if got.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want tls.VersionTLS13 (%#x)", got.MinVersion, tls.VersionTLS13)
	}
}

func TestLoadCredentialsReturnsAnErrorRatherThanExiting(t *testing.T) {
	tests := []struct {
		name string
		// corrupt breaks an otherwise-valid fixture directory.
		corrupt func(t *testing.T, dir string)
	}{
		{
			name: "the CA file is absent",
			corrupt: func(t *testing.T, dir string) {
				removeFile(t, dir, caFile)
			},
		},
		{
			name: "the CA file is present but is not PEM",
			corrupt: func(t *testing.T, dir string) {
				writeFile(t, dir, caFile, []byte("this is not a certificate"))
			},
		},
		{
			name: "the certificate file is absent",
			corrupt: func(t *testing.T, dir string) {
				removeFile(t, dir, serverCertFile)
			},
		},
		{
			name: "the key file is absent",
			corrupt: func(t *testing.T, dir string) {
				removeFile(t, dir, serverKeyFile)
			},
		},
		{
			name: "the certificate and the key do not match each other",
			corrupt: func(t *testing.T, dir string) {
				// Valid PEM that parses cleanly but is not the matching key.
				other := newAuthority(t)
				_, otherKey := other.issue(t, "someone-else")
				writeFile(t, dir, serverKeyFile, otherKey)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := validCertsDir(t)
			tt.corrupt(t, dir)

			if _, _, err := loadCredentials(dir, caFile, serverKeyFile, serverCertFile); err == nil {
				t.Error("loadCredentials returned no error for broken material")
			}

			conf, err := ServerTLSConfig(dir)
			if err == nil {
				t.Error("ServerTLSConfig returned no error for broken material")
			}
			if conf != nil {
				t.Errorf("ServerTLSConfig returned a non-nil config (%+v) alongside an error", conf)
			}
		})
	}
}

func TestLoadCredentialsReportsErrNoCACertificatesForNonPEM(t *testing.T) {
	dir := validCertsDir(t)
	writeFile(t, dir, caFile, []byte("-----BEGIN NOT A CERTIFICATE-----\nnope\n-----END NOT A CERTIFICATE-----\n"))

	_, _, err := loadCredentials(dir, caFile, serverKeyFile, serverCertFile)
	if !errors.Is(err, errNoCACertificates) {
		t.Errorf("loadCredentials err = %v, want errNoCACertificates", err)
	}
}

// A builder reading the wrong leaf file names would pass every test above.
func TestBothConfigBuildersFailOnTheirOwnMissingKeyPair(t *testing.T) {
	t.Run("server config needs the server key pair", func(t *testing.T) {
		dir := validCertsDir(t)
		removeFile(t, dir, serverCertFile)

		if _, err := ServerTLSConfig(dir); err == nil {
			t.Error("ServerTLSConfig succeeded without server-cert.pem")
		}
		if _, err := ClientTLSConfig(dir); err != nil {
			t.Errorf("ClientTLSConfig failed over a MISSING SERVER certificate: %v", err)
		}
	})

	t.Run("client config needs the client key pair", func(t *testing.T) {
		dir := validCertsDir(t)
		removeFile(t, dir, clientKeyFile)

		if _, err := ClientTLSConfig(dir); err == nil {
			t.Error("ClientTLSConfig succeeded without client-key.pem")
		}
		if _, err := ServerTLSConfig(dir); err != nil {
			t.Errorf("ServerTLSConfig failed over a MISSING CLIENT key: %v", err)
		}
	})
}

// The error's path is the only evidence of which directory was chosen.
func TestAnEmptyCertsDirFallsBackToDefaultCertsDir(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := ServerTLSConfig("")
	if err == nil {
		t.Fatal("ServerTLSConfig(\"\") succeeded in a directory with no cert/ subdirectory")
	}

	want := filepath.Join(DefaultCertsDir, caFile)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("ServerTLSConfig(\"\") err = %v, want it to name %q (the DefaultCertsDir fallback)", err, want)
	}
}

func removeFile(t *testing.T, dir, name string) {
	t.Helper()

	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatalf("remove %s: %v", name, err)
	}
}
