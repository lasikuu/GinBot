package server

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

type InstanceServer struct {
	ginbotv1connect.UnimplementedInstanceServiceHandler
}

func NewInstanceServer() *InstanceServer {
	s := &InstanceServer{}
	return s
}

func (s *InstanceServer) CreateInstance(ctx context.Context, connReq *connect.Request[pb.CreateInstanceReq]) (*connect.Response[pb.CreateInstanceResp], error) {
	req := connReq.Msg

	// Clearance is enforced ahead of this handler: the requirements map holds
	// CreateInstance at CLEARANCE_ADMINISTRATOR, so reaching here means the
	// caller has it.
	if !req.HasPlatformEnum() || !req.HasInstanceMeta() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("platform_enum and instance_meta are required"))
	}

	instanceID, err := db.CreateInstance(ctx, req.GetPlatformEnum(), req.GetInstanceMeta(), req.GetDefaultChannel())
	if err != nil {
		log.Z.Error("failed to create instance", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create instance"))
	}

	return connect.NewResponse(pb.CreateInstanceResp_builder{
		Id: &instanceID,
	}.Build()), nil
}

func (s *InstanceServer) GetInstance(ctx context.Context, connReq *connect.Request[pb.GetInstanceReq]) (*connect.Response[pb.GetInstanceResp], error) {
	req := connReq.Msg

	// Guarded at CLEARANCE_REGISTERED, so the interceptor has already parsed and
	// validated the caller's metadata; re-parsing it here would only repeat that.
	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	instance, err := db.GetInstanceByID(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("instance not found"))
	}
	if err != nil {
		log.Z.Error("failed to get instance", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get instance"))
	}

	return connect.NewResponse(pb.GetInstanceResp_builder{
		Instance: instance.ToProto(),
	}.Build()), nil
}

func (s *InstanceServer) ListInstances(_ context.Context, _ *connect.Request[pb.ListInstancesReq]) (*connect.Response[pb.ListInstancesResp], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ListInstances is not implemented yet"))
}

func (s *InstanceServer) UpdateInstance(_ context.Context, _ *connect.Request[pb.UpdateInstanceReq]) (*connect.Response[pb.UpdateInstanceResp], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("UpdateInstance is not implemented yet"))
}

func (s *InstanceServer) DeleteInstance(_ context.Context, _ *connect.Request[pb.DeleteInstanceReq]) (*connect.Response[pb.DeleteInstanceResp], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("DeleteInstance is not implemented yet"))
}
