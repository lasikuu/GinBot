package client

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc"
)

var RepostServiceClient pb.RepostServiceClient

func InitRepostService(conn *grpc.ClientConn) {
	RepostServiceClient = pb.NewRepostServiceClient(conn)
}
