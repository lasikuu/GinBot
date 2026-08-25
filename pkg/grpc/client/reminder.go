package client

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/grpc"
)

var ReminderServiceClient pb.ReminderServiceClient

func InitReminderService(conn *grpc.ClientConn) {
	ReminderServiceClient = pb.NewReminderServiceClient(conn)
}
