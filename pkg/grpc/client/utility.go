package client

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/grpc"
)

var UtilityServiceClient pb.UtilityServiceClient

func InitUtilityService(conn *grpc.ClientConn) {
	UtilityServiceClient = pb.NewUtilityServiceClient(conn)
}
