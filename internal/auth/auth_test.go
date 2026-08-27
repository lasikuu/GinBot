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

// TestMain gives this package a logger. LoadServerCredentials and
// LoadClientCredentials log through log.Z, which is nil until something
// initialises it.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	os.Exit(m.Run())
}

// The certificate material is MINTED BY THE TEST rather than read from the
// repository's own cert/ directory. cert/*.pem is gitignored and simply does
// not exist in CI or on a fresh clone, so a test that depended on it would be
// a test that never runs where it matters — and the whole point of this file
// is to pin a security property (MinVersion) that nobody would otherwise
// notice regressing.

// caFile and the leaf file names are the exact names loadCredentials joins
// onto certsDir. Spelled out here so a rename on the production side breaks
// these tests loudly instead of silently making every fixture invisible.
const (
	caFile         = "ca-cert.pem"
	serverCertFile = "server-cert.pem"
	serverKeyFile  = "server-key.pem"
	clientCertFile = "client-cert.pem"
	clientKeyFile  = "client-key.pem"
)

// authority is a self-signed CA plus the material needed to sign leaves.
type authority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

// newAuthority mints a self-signed P-256 CA. ECDSA rather than RSA purely for
// speed: this runs once per test and a 2048-bit RSA keygen is orders of
// magnitude slower for no additional coverage.
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

// issue mints a CA-signed leaf and returns its certificate and key in PEM.
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

// writeFile writes one fixture file, asserting the write.
func writeFile(t *testing.T, dir, name string, content []byte) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// validCertsDir builds a t.TempDir() containing a complete, internally
// consistent set of mutual TLS material under the exact names the production
// code expects.
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

// ── Happy paths ──────────────────────────────────────────────────────────────

// TestServerTLSConfigRequiresTLS13AndClientCertificates is the point of this
// file.
//
// MinVersion and ClientAuth are the two settings that decide whether the gRPC
// boundary between ginbot-server and its platform clients is actually mutually
// authenticated over a modern transport, and both are currently verified only
// by reading auth.go. Dropping MinVersion entirely still compiles, still
// connects, and silently permits TLS 1.0; dropping ClientAuth still compiles,
// still connects, and silently accepts any client at all.
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

// TestClientTLSConfigRequiresTLS13AndVerifiesTheServer, including the
// assertion that InsecureSkipVerify was not reached for: it is the single
// setting that turns mutual TLS into an unauthenticated tunnel, and it is a
// tempting "fix" for a certificate problem in development.
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

// TestLoadCredentialsReturnsTheKeyPairAndTheCAPool covers the shared helper
// directly, so the two config builders above are not the only witnesses to it.
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

// TestLoadServerTLSConfigReturnsAUsableConfig is the only test that touches
// the EXPORTED fatal loader. LoadClientCredentials and LoadServerCredentials
// are gone: stage 3 replaces them with this single fatal-on-failure loader
// plus the error-returning ServerTLSConfig/ClientTLSConfig pair above, which
// stage 4 (pkg/grpc/client) is the specified caller of.
//
// This is driven with a valid fixture exclusively and never with bad input:
// every failure path inside it ends at log.Z.Fatal, which calls os.Exit(1)
// and takes the entire test binary down with it — the failure paths are
// covered through the error-returning ServerTLSConfig seam instead.
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

// ── Failure paths ────────────────────────────────────────────────────────────

// TestLoadCredentialsReturnsAnErrorRatherThanExiting covers every way the
// material on disk can be wrong. The shared property being pinned is that each
// one produces an ERROR: before the error-returning seam existed these were
// all log.Z.Fatal, so a mistyped path in a deployment killed the process at
// boot with no chance for a caller to report it usefully.
func TestLoadCredentialsReturnsAnErrorRatherThanExiting(t *testing.T) {
	tests := []struct {
		name string
		// corrupt mutates an otherwise-valid fixture directory into the
		// broken state under test.
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
				// A second, independently issued leaf: its key is valid PEM
				// and parses cleanly, it simply is not the key for the
				// certificate sitting next to it. tls.LoadX509KeyPair is what
				// has to catch this, and it is the realistic operator error —
				// regenerating one half of the pair and copying only it.
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

			// The two config builders wrap the same helper, so both must
			// surface the failure rather than returning a half-built
			// *tls.Config a caller might use anyway.
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

// TestLoadCredentialsReportsErrNoCACertificatesForNonPEM: the CA pool failure
// is the one that x509 signals with a bare `false` rather than an error, so
// without a sentinel there is nothing to distinguish "the file is not a
// certificate" from "the file is missing" at a call site. errors.Is is used
// rather than a string match so the sentinel can be wrapped with context.
func TestLoadCredentialsReportsErrNoCACertificatesForNonPEM(t *testing.T) {
	dir := validCertsDir(t)
	writeFile(t, dir, caFile, []byte("-----BEGIN NOT A CERTIFICATE-----\nnope\n-----END NOT A CERTIFICATE-----\n"))

	_, _, err := loadCredentials(dir, caFile, serverKeyFile, serverCertFile)
	if !errors.Is(err, errNoCACertificates) {
		t.Errorf("loadCredentials err = %v, want errNoCACertificates", err)
	}
}

// TestBothConfigBuildersFailOnTheirOwnMissingKeyPair: each builder names a
// different pair of leaf files, and a directory that is valid for one but
// missing the other's material must fail only for the one that is actually
// broken. Without this, a builder reading the wrong file names would still
// pass every test above.
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

// TestAnEmptyCertsDirFallsBackToDefaultCertsDir.
//
// GINBOT_CERTS_PATH is parsed into config but is currently unused, so in
// practice every deployment runs on the DefaultCertsDir fallback and therefore
// depends on the process's working directory being the repository root. The
// working directory is switched to an empty one so cert/ is guaranteed absent,
// and the resulting error is inspected for the path it actually reached for —
// which is the only observable evidence of which directory the fallback chose.
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

// removeFile deletes one fixture file, asserting the removal so a test cannot
// silently keep exercising the valid path.
func removeFile(t *testing.T, dir, name string) {
	t.Helper()

	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatalf("remove %s: %v", name, err)
	}
}
