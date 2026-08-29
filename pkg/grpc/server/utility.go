package server

import (
	"context"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
)

// pongMessage carries no timing; the client measures the round trip itself.
const pongMessage = "pong"

// HealthProbe is injected so HealthCheck, grpchealth and /healthz cannot disagree.
type HealthProbe func(ctx context.Context) error

type UtilityServer struct {
	ginbotv1connect.UnimplementedUtilityServiceHandler

	probe HealthProbe
}

// NewUtilityServer accepts a nil probe, which always reports healthy.
func NewUtilityServer(probe HealthProbe) *UtilityServer {
	return &UtilityServer{probe: probe}
}

func (s *UtilityServer) HealthCheck(ctx context.Context, _ *connect.Request[pb.HealthCheckReq]) (*connect.Response[pb.HealthCheckResp], error) {
	healthStatus := pb.HealthStatus_HEALTH_STATUS_OK
	if s.probe != nil {
		if err := s.probe(ctx); err != nil {
			healthStatus = pb.HealthStatus_HEALTH_STATUS_ERROR
		}
	}

	return connect.NewResponse(pb.HealthCheckResp_builder{
		Status: &healthStatus,
	}.Build()), nil
}

func (s *UtilityServer) Ping(context.Context, *connect.Request[pb.PingReq]) (*connect.Response[pb.PingResp], error) {
	message := pongMessage

	return connect.NewResponse(pb.PingResp_builder{
		Message: &message,
	}.Build()), nil
}
