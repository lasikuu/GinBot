package server

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// pongMessage is what Ping answers with. The value is not the measurement: the
// client stamps the clock either side of the call and reports the difference,
// which needs no clock agreement between the two processes. A server-side
// timestamp diffed against a client-supplied one would report clock skew as
// latency.
const pongMessage = "pong"

type UtilityServer struct {
	pb.UnimplementedUtilityServiceServer
}

func NewUtilityServer() *UtilityServer {
	s := &UtilityServer{}
	return s
}

func (s *UtilityServer) HealthCheck(context.Context, *pb.HealthCheckReq) (*pb.HealthCheckResp, error) {
	// TODO: Implement health check for Discord, DB, and other services and return accordingly.
	status := pb.HealthStatus_HEALTH_STATUS_OK

	return pb.HealthCheckResp_builder{
		Status: &status,
	}.Build(), nil
}

// Ping answers as cheaply as it can, so that what the client measures is the
// round trip rather than any work done here.
func (s *UtilityServer) Ping(context.Context, *pb.PingReq) (*pb.PingResp, error) {
	message := pongMessage

	return pb.PingResp_builder{
		Message: &message,
	}.Build(), nil
}
