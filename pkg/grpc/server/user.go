package server

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type UserServer struct {
	pb.UnimplementedUserServiceServer
}

func NewUserServer() *UserServer {
	s := &UserServer{}
	return s
}

func (s *UserServer) GetCongratulableBirthdays(ctx context.Context, _ *emptypb.Empty) (*pb.GetCongratulableBirthdaysResp, error) {
	return nil, nil
}
