package server

import (
	"context"

	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/proto"
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

func (s *UserServer) Register(_ context.Context, req *pb.RegisterReq) (*pb.RegisterResp, error) {
	if req.Username == nil || req.Platform == nil || req.PlatformUserId == nil {
		return nil, status.Errorf(codes.InvalidArgument, "username, platform, and platform_user_id are required.")
	}

	userId, err := db.CreateUser(*req.Username, *req.Platform, *req.PlatformUserId, req.PlatformMetadata, req.Locale)
	if err != nil {
		return nil, err
	}

	return &pb.RegisterResp{
		UserId: userId,
	}, nil
}

func (s *UserServer) GetCongratulableBirthdays(ctx context.Context, _ *emptypb.Empty) (*pb.GetCongratulableBirthdaysResp, error) {
	return nil, nil
}
