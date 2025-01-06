package server

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type UtilityServer struct {
	pb.UnimplementedUtilityServiceServer
}

func NewUtilityServer() *UtilityServer {
	s := &UtilityServer{}
	return s
}

func (s *UtilityServer) HealthCheck(context.Context, *emptypb.Empty) (*pb.HealthCheckResp, error) {
	// TODO: Implement health check for Discord, DB, and other services and return accordingly.
	status := pb.HealthStatus_HEALTH_STATUS_OK
	return &pb.HealthCheckResp{
		Status: &status,
	}, nil
}
