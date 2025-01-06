package discord

import (
	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

var UserServiceClient pb.UserServiceClient
var UtilityServiceClient pb.UtilityServiceClient
var ReminderServiceClient pb.ReminderServiceClient
var EntertainmentServiceClient pb.EntertainmentServiceClient

func NewDiscordClient() {
	serverAddress := config.Options.GRPC.Host + ":" + config.Options.GRPC.Port

	conn, err := grpc.NewClient(serverAddress, config.Options.Discord.GRPCClientOptions.DialOptions...)
	if err != nil {
		log.Z.Fatal("failed to connect to gRPC server.", zap.Error(err))
		return
	}

	UserServiceClient = pb.NewUserServiceClient(conn)
	UtilityServiceClient = pb.NewUtilityServiceClient(conn)
	ReminderServiceClient = pb.NewReminderServiceClient(conn)
	EntertainmentServiceClient = pb.NewEntertainmentServiceClient(conn)
}
