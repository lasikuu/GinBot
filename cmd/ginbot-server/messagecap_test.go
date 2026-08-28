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
// TriggerService used to be raised above the baseline, to config.MaxGRPCMessageBytes,
// because GetFile returned a whole file's bytes inline in one unary message.
// That stopped being true this stage: GetFile is server-streaming now
// (pkg/grpc/server/trigger.go), sending GetFileChunkBytes-sized chunks rather
// than the whole file in one message, so nothing about TriggerService still
// needs a raised cap — the raise, config.MaxGRPCMessageBytes and
// triggerHandlerOpts are all gone from main.go. Every mounted service,
// TriggerService included, now goes through the SAME handlerOpts.
//
// These tests pin both halves: the baseline exists, and TriggerService is
// held to it exactly like everything else.

// oversizedBody is a Connect unary request body one byte over n.
//
// A unary body is the bare serialised message, NOT an envelope — envelopes and
// application/connect+proto are the streaming form, and sending one to a unary
// procedure gets 415 before the size is ever looked at.
func oversizedBody(n int) string {
	return strings.Repeat("\x00", n+1)
}

// mountedForCapTest builds a mux the way main() does, with the SAME option set
// for every service, over servers that need no database.
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

// TestTriggerServiceIsCappedAtTheBaseline is the replacement for the
// pre-stage-5 TestTriggerServiceIsRaisedAboveTheBaseline: TriggerService is no
// longer raised above baselineMessageBytes at all. GetFile stopped needing
// the raise when it stopped returning a whole file inline in one message
// (pkg/grpc/server/trigger.go's GetFileChunkBytes streaming), and nothing else
// on TriggerService ever needed one, so main.go now mounts it with exactly
// the same handlerOpts as every other service.
//
// Driven against TriggerService/TryTrigger, not GetFile: GetFile is
// server-streaming this stage, and postEnvelope's raw unary-envelope POST — a
// bare serialised message, not connect's streaming frame format — is not a
// request shape a streaming procedure ever accepts, so sending one there would
// fail on protocol grounds before the size cap was ever consulted and prove
// nothing about the cap. TryTrigger is an ordinary unary procedure on the same
// service and the same handlerOpts apply to both.
func TestTriggerServiceIsCappedAtTheBaseline(t *testing.T) {
	srv := mountedForCapTest(t)

	status := postEnvelope(t, srv.URL+ginbotv1connect.TriggerServiceTryTriggerProcedure, oversizedBody(baselineMessageBytes))
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d (resource_exhausted): TriggerService accepted a message "+
			"over the %d-byte baseline, so it is not capped the same way every other service is",
			status, http.StatusTooManyRequests, baselineMessageBytes)
	}
}

// TestBaselineMessageCapExceedsTheGetFileChunkSize pins the relationship that
// deleting config.MaxGRPCMessageBytes turned from a shared constant into two
// numbers written down in two packages.
//
// TriggerService is no longer raised above the baseline, so the ONLY thing
// keeping GetFile working is that a single GetFileChunk is smaller than the
// cap every service is now held to. Lower baselineMessageBytes below
// server.GetFileChunkBytes, or raise the chunk size above it, and every trigger
// file playback fails at the transport with resource_exhausted — for every
// file, not just large ones, since even a one-chunk file sends a full-size
// frame when the blob is big enough.
//
// The integration suite proves this end to end
// (TestGetFileStreamsAFileLargerThanTheBaselineMessageCap), but that needs
// Postgres. This is the cheap guard that fails on `go test ./...`.
func TestBaselineMessageCapExceedsTheGetFileChunkSize(t *testing.T) {
	if baselineMessageBytes <= server.GetFileChunkBytes {
		t.Errorf("baselineMessageBytes = %d, want strictly greater than server.GetFileChunkBytes (%d); "+
			"a single GetFileChunk would be refused by the cap this binary installs",
			baselineMessageBytes, server.GetFileChunkBytes)
	}
}
