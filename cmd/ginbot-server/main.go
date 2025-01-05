package main

import (
	"fmt"
	"net"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/enum"
	pb "github.com/lasikuu/GinBot/pkg/gen/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	config.SetEnv()
	db.InitDB()
	defer db.CloseDB()
	db.EnsureLatestVersion()

	log.Z.Info("starting GinBot with gRPC.", zap.String("host", config.Options.GRPC.Host), zap.String("port", config.Options.GRPC.Port))

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%s", config.Options.GRPC.Host, config.Options.GRPC.Port))
	if err != nil {
		log.Z.Fatal("failed to listen.", zap.Error(err))
	}

	grpcServer := grpc.NewServer(config.Options.GRPC.ServerOptions...)
	pb.RegisterUserServiceServer(grpcServer, server.NewUserServer())
	pb.RegisterUtilityServiceServer(grpcServer, server.NewUtilityServer())
	pb.RegisterReminderServiceServer(grpcServer, server.NewReminderServer())
	pb.RegisterAnalyticsServiceServer(grpcServer, server.NewAnalyticsServer())

	// Register reflection service on gRPC server.
	if config.AppEnvironment == enum.DEVELOPMENT {
		reflection.Register(grpcServer)
	}

	if err := grpcServer.Serve(lis); err != nil {
		log.Z.Fatal("failed to serve.", zap.Error(err))
		return
	}
}
