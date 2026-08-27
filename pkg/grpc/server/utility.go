package server

import (
	"context"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
)

// pongMessage is what Ping answers with. The value is not the measurement: the
// client stamps the clock either side of the call and reports the difference,
// which needs no clock agreement between the two processes. A server-side
// timestamp diffed against a client-supplied one would report clock skew as
// latency.
const pongMessage = "pong"

// HealthProbe reports whether the server can currently serve traffic. It is
// injected rather than hardcoded to Postgres so UtilityServer stays testable
// without a database, and so cmd/ginbot-server can share exactly one probe
// between UtilityService/HealthCheck, the gRPC health protocol and the plain
// GET /healthz the compose healthcheck polls — three surfaces asking the same
// question must not be able to disagree about the answer.
type HealthProbe func(ctx context.Context) error

type UtilityServer struct {
	ginbotv1connect.UnimplementedUtilityServiceHandler

	probe HealthProbe
}

// NewUtilityServer returns a UtilityServer backed by probe. A nil probe is
// accepted and always reports healthy, which keeps existing callers that have
// no probe to offer (tests, most of all) working without one.
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

// Ping answers as cheaply as it can, so that what the client measures is the
// round trip rather than any work done here.
func (s *UtilityServer) Ping(context.Context, *connect.Request[pb.PingReq]) (*connect.Response[pb.PingResp], error) {
	message := pongMessage

	return connect.NewResponse(pb.PingResp_builder{
		Message: &message,
	}.Build()), nil
}
