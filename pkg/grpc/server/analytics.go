package server

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AnalyticsServer struct {
	pb.UnimplementedAnalyticsServiceServer
}

func NewAnalyticsServer() *AnalyticsServer {
	s := &AnalyticsServer{}
	return s
}

// CreateActionRecord is not implemented yet: there is no action_record table.
// It arrives with the trigger stats work.
func (s *AnalyticsServer) CreateActionRecord(_ context.Context, _ *pb.CreateActionRecordReq) (*pb.CreateActionRecordResp, error) {
	return nil, status.Error(codes.Unimplemented, "CreateActionRecord is not implemented yet")
}

func (s *AnalyticsServer) ListActionRecords(_ context.Context, _ *pb.ListActionRecordsReq) (*pb.ListActionRecordsResp, error) {
	return nil, status.Error(codes.Unimplemented, "ListActionRecords is not implemented yet")
}
