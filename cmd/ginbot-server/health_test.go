package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
)

// healthChecker, GET /healthz, connectrpc.com/grpchealth and
// UtilityService/HealthCheck are the three surfaces this process exposes for
// "is it healthy" — the doc comment on healthChecker says all three are
// "backed from one probe". This file is the test for that claim, and it
// tests the ACTUAL wiring components main() assembles (healthChecker itself,
// plus a server.UtilityServer built the same way service.InitServices builds
// it) rather than main() itself, which is untested by design: main() cannot
// be invoked from a test without a real listener, a real database and a real
// signal handler racing the test binary's own.
//
// The health probe is injected as a plain func literal rather than requiring
// Postgres, exactly as server.HealthProbe is designed to allow.

func healthyProbe(context.Context) error { return nil }

func failingProbe(context.Context) error { return errors.New("connection refused") }

// TestHealthCheckerReportsServingWhenProbeSucceeds covers /healthz and the
// grpchealth protocol together, since both read the same healthChecker.
func TestHealthCheckerReportsServingWhenProbeSucceeds(t *testing.T) {
	checker := newHealthChecker(healthyProbe)

	resp, err := checker.Check(context.Background(), &grpchealth.CheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpchealth.StatusServing {
		t.Errorf("grpchealth status = %v, want %v", resp.Status, grpchealth.StatusServing)
	}

	rec := httptest.NewRecorder()
	checker.healthzHandler(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestHealthCheckerReportsNotServingWhenProbeFails: a database that cannot be
// reached must produce NOT_SERVING on the gRPC health protocol and a non-200
// on /healthz — the compose healthcheck only understands the latter, and a
// load balancer or the gRPC health protocol only understands the former, so
// both have to independently reflect the same failure.
func TestHealthCheckerReportsNotServingWhenProbeFails(t *testing.T) {
	checker := newHealthChecker(failingProbe)

	resp, err := checker.Check(context.Background(), &grpchealth.CheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpchealth.StatusNotServing {
		t.Errorf("grpchealth status = %v, want %v", resp.Status, grpchealth.StatusNotServing)
	}

	rec := httptest.NewRecorder()
	checker.healthzHandler(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("/healthz status = %d, want a non-200 status for an unreachable probe", rec.Code)
	}
}

// TestHealthCheckerReportsNotServingDuringShutdownRegardlessOfProbe is the
// property newHealthChecker's doc comment states explicitly: shutdown
// overrides the probe. A database that is still perfectly reachable is not a
// reason for a load balancer to keep routing new work to a process on its way
// out, so both /healthz and the gRPC health protocol have to flip to
// NOT_SERVING the instant shutdown begins — independent of what probe() would
// say if asked.
func TestHealthCheckerReportsNotServingDuringShutdownRegardlessOfProbe(t *testing.T) {
	checker := newHealthChecker(healthyProbe)
	checker.shutdown()

	resp, err := checker.Check(context.Background(), &grpchealth.CheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpchealth.StatusNotServing {
		t.Errorf("grpchealth status during shutdown = %v, want %v (probe is healthy but that must not matter)",
			resp.Status, grpchealth.StatusNotServing)
	}

	rec := httptest.NewRecorder()
	checker.healthzHandler(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("/healthz status during shutdown = %d, want a non-200 status", rec.Code)
	}
}

// TestShutdownIsIdempotentAndOneWay: shutdown is reachable from exactly one
// place in main() and called at most once in practice, but nothing prevents a
// second call, and there is deliberately no way back to serving — a process
// that decided it is shutting down does not un-decide that.
func TestShutdownIsIdempotentAndOneWay(t *testing.T) {
	checker := newHealthChecker(healthyProbe)

	checker.shutdown()
	checker.shutdown()

	resp, err := checker.Check(context.Background(), &grpchealth.CheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpchealth.StatusNotServing {
		t.Errorf("status after a repeated shutdown = %v, want %v", resp.Status, grpchealth.StatusNotServing)
	}
}

// TestNilProbeReportsHealthy: NewUtilityServer's own doc says a nil probe is
// accepted and always reports healthy, for callers that have none to offer.
// healthChecker mirrors that on the Check path (probe == nil is skipped
// entirely), and this pins it so the two health-surface implementations do
// not silently diverge on the one input neither of them actually receives in
// production — cmd/ginbot-server always supplies db.Ping — but which cheaply
// guards against a future caller that does not.
func TestNilProbeReportsHealthy(t *testing.T) {
	checker := newHealthChecker(nil)

	resp, err := checker.Check(context.Background(), &grpchealth.CheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpchealth.StatusServing {
		t.Errorf("status with a nil probe = %v, want %v", resp.Status, grpchealth.StatusServing)
	}
}

// TestUtilityServiceHealthCheckSharesTheSameProbeAsHealthChecker is the
// "same probe" half of healthChecker's doc comment: cmd/ginbot-server passes
// the identical probe function value to both newHealthChecker (for /healthz
// and the gRPC health protocol) and server.NewUtilityServer (for
// UtilityService/HealthCheck) via service.InitServices(db.Ping). This
// reconstructs that exact pairing with a fake probe and asserts both
// surfaces answer the same way for the same probe state — the property that
// stops the three surfaces from ever disagreeing about whether the process is
// healthy.
//
// It is NOT the same claim as the shutdown test above: UtilityServer has no
// wiring to healthChecker's shuttingDown flag at all — only /healthz and the
// gRPC health protocol observe shutdown, which is exactly what "the same
// probe" promises and no more. See the interface-assumptions note in the test
// report for why that distinction matters.
func TestUtilityServiceHealthCheckSharesTheSameProbeAsHealthChecker(t *testing.T) {
	tests := []struct {
		name  string
		probe server.HealthProbe
		want  pb.HealthStatus
	}{
		{"healthy probe", healthyProbe, pb.HealthStatus_HEALTH_STATUS_OK},
		{"failing probe", failingProbe, pb.HealthStatus_HEALTH_STATUS_ERROR},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := newHealthChecker(tt.probe)
			utility := server.NewUtilityServer(tt.probe)

			grpcResp, err := checker.Check(context.Background(), &grpchealth.CheckRequest{})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			wantGRPCStatus := grpchealth.StatusServing
			if tt.want == pb.HealthStatus_HEALTH_STATUS_ERROR {
				wantGRPCStatus = grpchealth.StatusNotServing
			}
			if grpcResp.Status != wantGRPCStatus {
				t.Errorf("grpchealth status = %v, want %v", grpcResp.Status, wantGRPCStatus)
			}

			utilityResp, err := utility.HealthCheck(context.Background(), connect.NewRequest(pb.HealthCheckReq_builder{}.Build()))
			if err != nil {
				t.Fatalf("UtilityServer.HealthCheck: %v", err)
			}
			if got := utilityResp.Msg.GetStatus(); got != tt.want {
				t.Errorf("UtilityService/HealthCheck status = %v, want %v", got, tt.want)
			}
		})
	}
}
