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

type UserServer struct {
	pb.UnimplementedUserServiceServer
}

func NewUserServer() *UserServer {
	s := &UserServer{}
	return s
}

func (s *UserServer) Register(ctx context.Context, req *pb.RegisterReq) (*pb.RegisterResp, error) {
	if !(req.HasUsername() && req.HasPlatform() && req.HasPlatformUserId()) {
		return nil, status.Errorf(codes.InvalidArgument, "username, platform, and platform_user_id are required")
	}

	userID, err := db.CreateUser(
		ctx,
		req.GetUsername(),
		req.GetPlatform(),
		req.GetPlatformUserId(),
		req.GetPlatformMetadata(),
		req.GetLocale(),
	)
	if errors.Is(err, db.ErrAlreadyExists) {
		return nil, status.Errorf(codes.AlreadyExists, "this platform identity is already registered")
	}
	if err != nil {
		log.Z.Error("failed to create user", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to create user")
	}

	return pb.RegisterResp_builder{
		UserId: &userID,
	}.Build(), nil
}

func (s *UserServer) GetUser(ctx context.Context, req *pb.GetUserReq) (*pb.GetUserResp, error) {
	meta, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, status.Errorf(codes.InvalidArgument, "id is required")
	}

	if meta.PlatformUID == nil {
		return nil, status.Errorf(codes.InvalidArgument, "user_id metadata is required")
	}

	caller, err := db.GetUserByPlatformUID(ctx, meta.PlatformEnum, *meta.PlatformUID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, status.Errorf(codes.FailedPrecondition, "caller is not registered")
	}
	if err != nil {
		log.Z.Error("failed to resolve caller", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to resolve caller")
	}

	// A user row carries locale, timezone and birthday, so it is only readable by
	// its owner. Reading another user will become possible for elevated clearance
	// once the clearance interceptor exists; until then, self only.
	if req.GetId() != caller.ID {
		return nil, status.Errorf(codes.PermissionDenied, "cannot read another user")
	}

	return pb.GetUserResp_builder{
		User: caller.ToProto(),
	}.Build(), nil
}

func (s *UserServer) GetCongratulableBirthdays(_ context.Context, _ *emptypb.Empty) (*pb.GetCongratulableBirthdaysResp, error) {
	return nil, status.Error(codes.Unimplemented, "GetCongratulableBirthdays is not implemented yet")
}
