package server

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

type AnalyticsServer struct {
	pb.UnimplementedAnalyticsServiceServer
}

func NewAnalyticsServer() *AnalyticsServer {
	s := &AnalyticsServer{}
	return s
}

func (s *AnalyticsServer) CreateActionRecord(ctx context.Context, req *pb.CreateActionRecordReq) (*pb.CreateActionRecordResp, error) {
	return nil, nil
}

func (s *AnalyticsServer) ListActionRecords(ctx context.Context, req *pb.ListActionRecordsReq) (*pb.ListActionRecordsResp, error) {
	return nil, nil
}
