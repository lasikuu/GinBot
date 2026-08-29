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

// The components main() assembles, since main() needs a real listener and DB.

func healthyProbe(context.Context) error { return nil }

func failingProbe(context.Context) error { return errors.New("connection refused") }

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

// UtilityServer has no wiring to shuttingDown; only the probe is shared.
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
