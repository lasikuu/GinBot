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

// CreateActionRecord persists one analytics action attributed to the CALLER.
//
// The request cannot name an actor: identity comes from metadata and never from
// a request field, which is the project-wide rule. The field that used to be
// there (reserved 3 in analytics.proto) let any CLEARANCE_REGISTERED caller
// attribute any action type to any user_account UUID, and — since it carried no
// uuid constraint — turn arbitrary text into a codes.Internal from a failed
// Postgres cast.
//
// The server's own reminder paths (REMINDER_CREATED, REMINDER_DELIVERED) go
// straight to db.CreateActionRecord in-process rather than through this RPC, so
// they can still attribute a system-initiated action to the right user without
// this restriction getting in the way.
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
