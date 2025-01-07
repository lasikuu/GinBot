package client

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc"
)

var EntertainmentServiceClient pb.EntertainmentServiceClient

func InitEntertainmentService(conn *grpc.ClientConn) {
	EntertainmentServiceClient = pb.NewEntertainmentServiceClient(conn)
}
