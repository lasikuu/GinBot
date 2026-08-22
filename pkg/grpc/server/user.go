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
	if err != nil {
		log.Z.Error("failed to create user", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to create user")
	}

	return pb.RegisterResp_builder{
		UserId: &userID,
	}.Build(), nil
}

func (s *UserServer) GetUser(ctx context.Context, req *pb.GetUserReq) (*pb.GetUserResp, error) {
	if !req.HasId() {
		return nil, status.Errorf(codes.InvalidArgument, "id is required")
	}

	user, err := db.GetUser(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "user not found")
	}
	if err != nil {
		log.Z.Error("failed to get user", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get user")
	}

	return pb.GetUserResp_builder{
		User: user.ToProto(),
	}.Build(), nil
}

func (s *UserServer) GetCongratulableBirthdays(_ context.Context, _ *emptypb.Empty) (*pb.GetCongratulableBirthdaysResp, error) {
	return nil, status.Error(codes.Unimplemented, "GetCongratulableBirthdays is not implemented yet")
}
