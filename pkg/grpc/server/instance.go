package server

import (
	"context"
	"errors"

	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
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

func (s *InstanceServer) CreateInstance(ctx context.Context, req *pb.CreateInstanceReq) (*pb.CreateInstanceResp, error) {
	// Caller identity is required on every identity-bearing RPC per AGENTS.md.
	// Restricting instance administration to elevated clearance needs the
	// clearance interceptor, which does not exist yet.
	if _, err := getMetadata(ctx); err != nil {
		return nil, err
	}

	if !(req.HasPlatformEnum() && req.HasInstanceMeta()) {
		return nil, status.Errorf(codes.InvalidArgument, "platform_enum and instance_meta are required")
	}

	instanceID, err := db.CreateInstance(ctx, req.GetPlatformEnum(), req.GetInstanceMeta(), req.GetDefaultChannel())
	if err != nil {
		log.Z.Error("failed to create instance", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to create instance")
	}

	return pb.CreateInstanceResp_builder{
		Id: &instanceID,
	}.Build(), nil
}

func (s *InstanceServer) GetInstance(ctx context.Context, req *pb.GetInstanceReq) (*pb.GetInstanceResp, error) {
	if _, err := getMetadata(ctx); err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, status.Errorf(codes.InvalidArgument, "id is required")
	}

	instance, err := db.GetInstanceByID(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "instance not found")
	}
	if err != nil {
		log.Z.Error("failed to get instance", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get instance")
	}

	return pb.GetInstanceResp_builder{
		Instance: instance.ToProto(),
	}.Build(), nil
}

func (s *InstanceServer) ListInstances(_ context.Context, _ *pb.ListInstancesReq) (*pb.ListInstancesResp, error) {
	return nil, status.Error(codes.Unimplemented, "ListInstances is not implemented yet")
}

func (s *InstanceServer) UpdateInstance(_ context.Context, _ *pb.UpdateInstanceReq) (*emptypb.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "UpdateInstance is not implemented yet")
}

func (s *InstanceServer) DeleteInstance(_ context.Context, _ *pb.DeleteInstanceReq) (*emptypb.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "DeleteInstance is not implemented yet")
}
