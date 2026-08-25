package client

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/grpc"
)

var TriggerServiceClient pb.TriggerServiceClient

func InitTriggerService(conn *grpc.ClientConn) {
	TriggerServiceClient = pb.NewTriggerServiceClient(conn)
}
