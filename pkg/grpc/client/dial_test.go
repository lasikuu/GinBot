package client

import (
	"net"
	"testing"
)

// ── validateBaseURL / Dial's base URL rejection ──────────────────────────────
//
// validateBaseURL exists so a malformed base URL is refused at Dial time
// rather than accepted and then failing every subsequent RPC with no
// indication why (see its doc comment in dial.go). Nothing in the repository
// called Dial before this file, so nothing had ever exercised this check —
// dialBadURLCases below drives it directly through Dial rather than in
// isolation, so a regression that broke the WIRING between Dial and
// validateBaseURL (rather than validateBaseURL itself) would still be caught.

// realWorldMistakeBaseURL reproduces the misconfiguration validateBaseURL's
// own doc comment cites: GINBOT_GRPC_HOST=http://foo, run through the SAME
// construction internal/config.GRPCServerOptions.ClientBaseURL uses —
// "http://" + net.JoinHostPort(host, port) — rather than the doc comment's
// illustrative (and slightly simplified) "http://http://foo:50051".
//
// net.JoinHostPort brackets a host containing a colon, the way a literal
// IPv6 address needs to be bracketed, and "http://foo" contains one — from
// its own "http:" prefix. So the actual string produced by this exact
// misconfiguration is "http://[http://foo]:50051", not the unbracketed
// doubled-scheme form the comment shows; pkg/grpc/client is deliberately
// isolated from internal/config (see dial.go's package doc), so this
// reproduces that construction by hand rather than importing it.
func realWorldMistakeBaseURL() string {
	return "http://" + net.JoinHostPort("http://foo", "50051")
}

func TestDialRejectsAMalformedBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{"missing scheme", "localhost:50051"},
		{"wrong scheme", "ftp://foo:50051"},
		{"empty host", "http://"},
		{"empty string", ""},
		{"the GINBOT_GRPC_HOST=http://foo real-world mistake", realWorldMistakeBaseURL()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clients, err := Dial(Options{BaseURL: tc.baseURL})

			if err == nil {
				t.Fatalf("Dial(%q) returned no error; a malformed base URL must be refused at dial time rather than accepted and failing every later RPC", tc.baseURL)
			}
			if clients != nil {
				t.Errorf("Dial(%q) returned a non-nil *Clients alongside its error", tc.baseURL)
			}
		})
	}
}

func TestDialAcceptsAWellFormedBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{"http", "http://localhost:50051"},
		{"https", "https://localhost:50051"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clients, err := Dial(Options{BaseURL: tc.baseURL})
			if err != nil {
				t.Fatalf("Dial(%q) = %v, want no error for a well-formed base URL", tc.baseURL, err)
			}
			if clients == nil {
				t.Fatal("Dial returned a nil *Clients alongside a nil error")
			}
			clients.Close()
		})
	}
}
