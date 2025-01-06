package server

import (
	"context"

	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type InstanceServer struct {
	pb.UnimplementedInstanceServiceServer
}

func NewInstanceServer() *InstanceServer {
	s := &InstanceServer{}
	return s
}

func (s *InstanceServer) CreateInstance(_ context.Context, req *pb.CreateInstanceReq) (*pb.CreateInstanceResp, error) {
	if req.PlatformEnum == nil || req.InstanceMeta == nil {
		return nil, status.Errorf(codes.InvalidArgument, "enum and meta are required.")
	}

	platformId, err := db.CreateInstance(*req.PlatformEnum, req.InstanceMeta, req.DefaultChannel)
	if err != nil {
		return nil, err
	}

	return &pb.CreateInstanceResp{
		Id: &platformId,
	}, nil
}

func (s *InstanceServer) GetInstance(_ context.Context, req *pb.GetInstanceReq) (*pb.GetInstanceResp, error) {
	return nil, nil
}

func (s *InstanceServer) ListInstances(_ context.Context, req *pb.ListInstancesReq) (*pb.ListInstancesResp, error) {
	return nil, nil
}

func (s *InstanceServer) UpdateInstance(_ context.Context, req *pb.UpdateInstanceReq) (*emptypb.Empty, error) {
	return nil, nil
}

func (s *InstanceServer) DeleteInstance(_ context.Context, req *pb.DeleteInstanceReq) (*emptypb.Empty, error) {
	return nil, nil
}
