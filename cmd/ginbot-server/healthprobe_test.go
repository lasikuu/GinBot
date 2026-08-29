package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/internal/auth"
)

// Certificate material is minted here: cert/*.pem is gitignored.

type testAuthority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func newTestAuthority(t *testing.T) *testAuthority {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ginbot-probe-test-ca"},
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

	return &testAuthority{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// Both names are needed: the probe dials healthProbeHost, httptest binds 127.0.0.1.
func (a *testAuthority) issue(t *testing.T, commonName string) (certPEM, keyPEM []byte) {
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
		DNSNames:     []string{healthProbeHost},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
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

// Uses the file names internal/auth expects, so the production loader runs.
func probeCertsDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	ca := newTestAuthority(t)

	serverCert, serverKey := ca.issue(t, "ginbot-server")
	clientCert, clientKey := ca.issue(t, "ginbot-client")

	for name, content := range map[string][]byte{
		"ca-cert.pem":     ca.certPEM,
		"server-cert.pem": serverCert,
		"server-key.pem":  serverKey,
		"client-cert.pem": clientCert,
		"client-key.pem":  clientKey,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	return dir
}

// tlsConf is nil for plaintext. The returned URL names healthProbeHost.
func healthzServer(t *testing.T, status int, tlsConf *tls.Config) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})

	srv := httptest.NewUnstartedServer(mux)
	if tlsConf != nil {
		srv.TLS = tlsConf
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)

	// Swapping httptest's host keeps the DNS SAN in the verification path.
	_, port, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://"))
	if err != nil {
		t.Fatalf("split %s: %v", srv.URL, err)
	}

	scheme := "http"
	if tlsConf != nil {
		scheme = "https"
	}

	return scheme + "://" + net.JoinHostPort(healthProbeHost, port) + "/healthz"
}

func probeContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	t.Cleanup(cancel)

	return ctx
}

func TestHealthProbeURLTracksTLS(t *testing.T) {
	tests := []struct {
		name string
		tls  bool
		port string
		want string
	}{
		{"plaintext", false, "50051", "http://localhost:50051/healthz"},
		{"tls", true, "50051", "https://localhost:50051/healthz"},
		{"non-default port", true, "9443", "https://localhost:9443/healthz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := healthProbeURL(tt.tls, tt.port); got != tt.want {
				t.Errorf("healthProbeURL(%v, %q) = %q, want %q", tt.tls, tt.port, got, tt.want)
			}
		})
	}
}

func TestProbeHealthOverPlaintext(t *testing.T) {
	url := healthzServer(t, http.StatusOK, nil)

	if err := probeHealth(probeContext(t), url, nil); err != nil {
		t.Errorf("probeHealth against a healthy plaintext server: %v", err)
	}
}

func TestProbeHealthFailsOnNon200(t *testing.T) {
	url := healthzServer(t, http.StatusServiceUnavailable, nil)

	err := probeHealth(probeContext(t), url, nil)
	if err == nil {
		t.Fatal("probeHealth against a 503 returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("probeHealth error = %v, want it to name the status", err)
	}
}

func TestProbeHealthFailsWhenNothingIsListening(t *testing.T) {
	// Bound then released, so the port is almost certainly free.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, err := net.SplitHostPort(lis.Addr().String())
	if err != nil {
		t.Fatalf("split %s: %v", lis.Addr(), err)
	}
	if err := lis.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := probeHealth(probeContext(t), healthProbeURL(false, port), nil); err == nil {
		t.Error("probeHealth against a closed port returned nil, want an error")
	}
}

// Uses the same config pair production does, so green also means mTLS works.
func TestProbeHealthOverMutualTLS(t *testing.T) {
	dir := probeCertsDir(t)

	serverConf, err := auth.ServerTLSConfig(dir)
	if err != nil {
		t.Fatalf("auth.ServerTLSConfig: %v", err)
	}
	clientConf, err := auth.ClientTLSConfig(dir)
	if err != nil {
		t.Fatalf("auth.ClientTLSConfig: %v", err)
	}

	url := healthzServer(t, http.StatusOK, serverConf)

	if err := probeHealth(probeContext(t), url, clientConf); err != nil {
		t.Errorf("probeHealth over mutual TLS: %v", err)
	}
}

// Passing with a nil client config means the server stopped requiring certs.
func TestPlaintextProbeCannotReachATLSServer(t *testing.T) {
	dir := probeCertsDir(t)

	serverConf, err := auth.ServerTLSConfig(dir)
	if err != nil {
		t.Fatalf("auth.ServerTLSConfig: %v", err)
	}

	tlsURL := healthzServer(t, http.StatusOK, serverConf)

	if err := probeHealth(probeContext(t), strings.Replace(tlsURL, "https://", "http://", 1), nil); err == nil {
		t.Error("a plaintext probe reached a TLS listener, want an error")
	}

	// Correct scheme, no client certificate: RequireAndVerifyClientCert rejects it.
	anonymous := &tls.Config{RootCAs: serverConf.ClientCAs, MinVersion: tls.VersionTLS13}
	if err := probeHealth(probeContext(t), tlsURL, anonymous); err == nil {
		t.Error("a probe with no client certificate was accepted, want an error")
	}
}
