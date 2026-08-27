package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
)

// The message caps this binary installs, and specifically the fact that
// omitting one is not a smaller cap but NO cap.
//
// connect.WithReadMaxBytes is stored as a plain int that the envelope reader
// only consults when it is above zero, so a handler mounted without it accepts
// a message of any size and buffers the whole thing before a single interceptor
// runs. The grpc-go server this replaced set MaxRecvMsgSize connection-wide, so
// the port could silently drop a bound that used to cover everything — and it
// did, until this was caught: a 30 MB body was accepted and persisted through
// UserService/Register, which is one of the three DELIBERATELY PUBLIC
// procedures, where no clearance interceptor runs and the peer choosing the
// allocation size has not authenticated at all.
//
// These tests pin both halves: the baseline exists, and TriggerService alone is
// raised above it.

// oversizedBody is a Connect unary request body one byte over n.
//
// A unary body is the bare serialised message, NOT an envelope — envelopes and
// application/connect+proto are the streaming form, and sending one to a unary
// procedure gets 415 before the size is ever looked at.
func oversizedBody(n int) string {
	return strings.Repeat("\x00", n+1)
}

// mountedForCapTest builds a mux the way main() does, with the same two option
// sets, over servers that need no database.
func mountedForCapTest(t *testing.T) *httptest.Server {
	t.Helper()

	handlerOpts := []connect.HandlerOption{
		connect.WithReadMaxBytes(baselineMessageBytes),
		connect.WithSendMaxBytes(baselineMessageBytes),
	}
	triggerHandlerOpts := append(
		append([]connect.HandlerOption{}, handlerOpts...),
		connect.WithReadMaxBytes(config.MaxGRPCMessageBytes),
		connect.WithSendMaxBytes(config.MaxGRPCMessageBytes),
	)

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewUtilityServiceHandler(server.NewUtilityServer(nil), handlerOpts...))
	mux.Handle(ginbotv1connect.NewTriggerServiceHandler(server.NewTriggerServer(), triggerHandlerOpts...))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

// postEnvelope sends a raw Connect unary envelope and reports the HTTP status.
// Raw rather than through a generated client, because a client would refuse to
// SEND an oversized message and the server-side cap would never be exercised.
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

// A service with no reason to carry a file must refuse an oversized message.
// UtilityService/Ping is the sharpest case available: it is public, so nothing
// else in the chain would stop the allocation.
func TestBaselineMessageCapRefusesAnOversizedMessage(t *testing.T) {
	srv := mountedForCapTest(t)

	status := postEnvelope(t, srv.URL+ginbotv1connect.UtilityServicePingProcedure, oversizedBody(baselineMessageBytes))

	// 429 is what Connect maps CodeResourceExhausted onto over HTTP.
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d (resource_exhausted): a message over the %d-byte "+
			"baseline was not refused, so every service mounted with handlerOpts is "+
			"accepting an attacker-chosen allocation",
			status, http.StatusTooManyRequests, baselineMessageBytes)
	}
}

// The baseline must not be so tight that ordinary traffic breaks. A real Ping
// is a few bytes, so this only fails if the cap were set absurdly low.
func TestBaselineMessageCapAcceptsAnOrdinaryMessage(t *testing.T) {
	srv := mountedForCapTest(t)

	client := ginbotv1connect.NewUtilityServiceClient(srv.Client(), srv.URL)
	if _, err := client.Ping(t.Context(), connect.NewRequest(pb.PingReq_builder{}.Build())); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TriggerService is raised above the baseline, because GetFile returns a file's
// bytes inline in one unary response. A message between the two limits proves
// the raise is actually applied and did not get lost behind the baseline that
// was added after it.
func TestTriggerServiceIsRaisedAboveTheBaseline(t *testing.T) {
	if config.MaxGRPCMessageBytes <= baselineMessageBytes {
		t.Fatalf("MaxGRPCMessageBytes (%d) is not above baselineMessageBytes (%d), so this "+
			"test cannot distinguish the two caps",
			config.MaxGRPCMessageBytes, baselineMessageBytes)
	}

	srv := mountedForCapTest(t)

	// Over the baseline, under the raised cap. It must NOT be refused for size;
	// it fails later, on decode, which is a different code.
	status := postEnvelope(t, srv.URL+ginbotv1connect.TriggerServiceGetFileProcedure, oversizedBody(baselineMessageBytes))
	if status == http.StatusTooManyRequests {
		t.Errorf("TriggerService refused a %d-byte message with resource_exhausted; it is "+
			"capped at the baseline rather than at MaxGRPCMessageBytes, so GetFile cannot "+
			"return a file of the size storage.MaxFileBytes permits", baselineMessageBytes+1)
	}

	// Over the raised cap. It must be refused.
	status = postEnvelope(t, srv.URL+ginbotv1connect.TriggerServiceGetFileProcedure, oversizedBody(config.MaxGRPCMessageBytes))
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d: TriggerService accepted a message over "+
			"MaxGRPCMessageBytes, so its cap is not applied at all",
			status, http.StatusTooManyRequests)
	}
}
