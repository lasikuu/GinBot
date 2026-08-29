package client

import (
	"net"
	"testing"
)

// realWorldMistakeBaseURL reproduces GINBOT_GRPC_HOST=http://foo run through the
// "http://" + net.JoinHostPort(host, port) construction config uses, which
// brackets the embedded colon to "http://[http://foo]:50051".
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
