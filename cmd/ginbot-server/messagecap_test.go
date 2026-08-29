package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
)

// Omitting a Connect cap is not a smaller cap but no cap: the envelope reader
// consults WithReadMaxBytes only when it is above zero.

// A bare serialised message, not an envelope; an envelope gets 415 first.
func oversizedBody(n int) string {
	return strings.Repeat("\x00", n+1)
}

func mountedForCapTest(t *testing.T) *httptest.Server {
	t.Helper()

	handlerOpts := []connect.HandlerOption{
		connect.WithReadMaxBytes(baselineMessageBytes),
		connect.WithSendMaxBytes(baselineMessageBytes),
	}

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewUtilityServiceHandler(server.NewUtilityServer(nil), handlerOpts...))
	mux.Handle(ginbotv1connect.NewTriggerServiceHandler(server.NewTriggerServer(), handlerOpts...))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

// Raw: a generated client would refuse to send an oversized message.
func postEnvelope(t *testing.T, url string, body string) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post envelope: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

// Ping is public, so nothing else in the chain would stop the allocation.
func TestBaselineMessageCapRefusesAnOversizedMessage(t *testing.T) {
	srv := mountedForCapTest(t)

	status := postEnvelope(t, srv.URL+ginbotv1connect.UtilityServicePingProcedure, oversizedBody(baselineMessageBytes))

	// 429 is Connect's HTTP mapping for CodeResourceExhausted.
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d (resource_exhausted): a message over the %d-byte "+
			"baseline was not refused, so every service mounted with handlerOpts is "+
			"accepting an attacker-chosen allocation",
			status, http.StatusTooManyRequests, baselineMessageBytes)
	}
}

func TestBaselineMessageCapAcceptsAnOrdinaryMessage(t *testing.T) {
	srv := mountedForCapTest(t)

	client := ginbotv1connect.NewUtilityServiceClient(srv.Client(), srv.URL)
	if _, err := client.Ping(t.Context(), connect.NewRequest(pb.PingReq_builder{}.Build())); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// Driven against TryTrigger, not GetFile: a unary POST to a server-streaming
// procedure fails on protocol grounds before the cap is consulted.
func TestTriggerServiceIsCappedAtTheBaseline(t *testing.T) {
	srv := mountedForCapTest(t)

	status := postEnvelope(t, srv.URL+ginbotv1connect.TriggerServiceTryTriggerProcedure, oversizedBody(baselineMessageBytes))
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d (resource_exhausted): TriggerService accepted a message "+
			"over the %d-byte baseline, so it is not capped the same way every other service is",
			status, http.StatusTooManyRequests, baselineMessageBytes)
	}
}

// A GetFileChunk over the baseline fails every trigger file playback.
func TestBaselineMessageCapExceedsTheGetFileChunkSize(t *testing.T) {
	if baselineMessageBytes <= server.GetFileChunkBytes {
		t.Errorf("baselineMessageBytes = %d, want strictly greater than server.GetFileChunkBytes (%d); "+
			"a single GetFileChunk would be refused by the cap this binary installs",
			baselineMessageBytes, server.GetFileChunkBytes)
	}
}
