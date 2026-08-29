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

type AnalyticsServer struct {
	ginbotv1connect.UnimplementedAnalyticsServiceHandler
}

func NewAnalyticsServer() *AnalyticsServer {
	s := &AnalyticsServer{}
	return s
}

// CreateActionRecord attributes the action to the caller; a request cannot name an actor.
func (s *AnalyticsServer) CreateActionRecord(ctx context.Context, connReq *connect.Request[pb.CreateActionRecordReq]) (*connect.Response[pb.CreateActionRecordResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasActionType() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("action_type is required"))
	}

	var actionTime *int64
	if req.HasActionTime() {
		value := req.GetActionTime()
		actionTime = &value
	}

	if err := db.CreateActionRecord(ctx, req.GetActionType(), caller.ID, actionTime); err != nil {
		log.Z.Error("failed to create action record", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create action record"))
	}

	return connect.NewResponse(pb.CreateActionRecordResp_builder{}.Build()), nil
}

func (s *AnalyticsServer) ListActionRecords(_ context.Context, _ *connect.Request[pb.ListActionRecordsReq]) (*connect.Response[pb.ListActionRecordsResp], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ListActionRecords is not implemented yet"))
}
