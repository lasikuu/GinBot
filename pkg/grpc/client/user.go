package client

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc"
)

var UserServiceClient pb.UserServiceClient

func InitUserService(conn *grpc.ClientConn) {
	UserServiceClient = pb.NewUserServiceClient(conn)
}
