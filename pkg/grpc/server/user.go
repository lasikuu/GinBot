package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// localTimezone loads successfully but resolves to the server process's own zone.
const localTimezone = "Local"

type UserServer struct {
	ginbotv1connect.UnimplementedUserServiceHandler
}

func NewUserServer() *UserServer {
	s := &UserServer{}
	return s
}

func (s *UserServer) Register(ctx context.Context, connReq *connect.Request[pb.RegisterReq]) (*connect.Response[pb.RegisterResp], error) {
	req := connReq.Msg

	// Register is public, so no interceptor stashed identity. The assertion is trusted.
	meta, err := callermeta.FromHeader(connReq.Header())
	if err != nil {
		return nil, err
	}

	if meta.PlatformUID == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s metadata is required", callermeta.HeaderUserID))
	}

	if !req.HasUsername() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("username is required"))
	}

	userID, err := db.CreateUser(
		ctx,
		req.GetUsername(),
		meta.PlatformEnum,
		*meta.PlatformUID,
		req.GetPlatformMetadata(),
		req.GetLocale(),
	)
	if errors.Is(err, db.ErrAlreadyExists) {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("this platform identity is already registered"))
	}
	if err != nil {
		log.Z.Error("failed to create user", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create user"))
	}

	return connect.NewResponse(pb.RegisterResp_builder{
		UserId: &userID,
	}.Build()), nil
}

func (s *UserServer) GetUser(ctx context.Context, connReq *connect.Request[pb.GetUserReq]) (*connect.Response[pb.GetUserResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	// An unset id means "me": a client never learns its own user_account UUID.
	if !req.HasId() || req.GetId() == caller.ID {
		return connect.NewResponse(pb.GetUserResp_builder{
			User: caller.ToProto(),
		}.Build()), nil
	}

	// A user row carries locale, timezone and birthday.
	if caller.Clearance < int32(pb.Clearance_CLEARANCE_MODERATOR) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("cannot read another user"))
	}

	user, err := db.GetUser(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
	}
	if err != nil {
		log.Z.Error("failed to get user", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get user"))
	}

	return connect.NewResponse(pb.GetUserResp_builder{
		User: user.ToProto(),
	}.Build()), nil
}

func (s *UserServer) SetLocale(ctx context.Context, connReq *connect.Request[pb.SetLocaleReq]) (*connect.Response[pb.SetLocaleResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	// protovalidate skips a rule on an unset field, so an absent locale passes it.
	if !req.HasLocale() || req.GetLocale() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("locale is required"))
	}

	if err := db.SetUserLocale(ctx, caller.ID, req.GetLocale()); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		log.Z.Error("failed to set locale", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to set locale"))
	}

	return connect.NewResponse(pb.SetLocaleResp_builder{}.Build()), nil
}

func (s *UserServer) SetTimezone(ctx context.Context, connReq *connect.Request[pb.SetTimezoneReq]) (*connect.Response[pb.SetTimezoneResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	// time.LoadLocation("") succeeds and returns UTC, so empty must be refused here.
	if !req.HasTimezone() || req.GetTimezone() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("timezone is required"))
	}

	timezone := req.GetTimezone()

	// protovalidate only checks non-empty, so an unresolvable zone would fail far later.
	if timezone == localTimezone {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("timezone must be a named IANA zone, such as Europe/Helsinki"))
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown timezone %q", timezone))
	}

	if err := db.SetUserTimezone(ctx, caller.ID, timezone); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		log.Z.Error("failed to set timezone", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to set timezone"))
	}

	return connect.NewResponse(pb.SetTimezoneResp_builder{}.Build()), nil
}

func (s *UserServer) GetCongratulableBirthdays(_ context.Context, _ *connect.Request[pb.GetCongratulableBirthdaysReq]) (*connect.Response[pb.GetCongratulableBirthdaysResp], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("GetCongratulableBirthdays is not implemented yet"))
}
