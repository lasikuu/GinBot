package main

import (
	"fmt"
	"net"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/cron"
	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/enum"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	// Environment variables and logger
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	config.SetEnv()

	// Database
	db.InitDB()
	defer db.CloseDB()
	db.EnsureLatestVersion()

	log.Z.Info("starting GinBot with gRPC.", zap.String("host", config.Options.GRPC.Host), zap.String("port", config.Options.GRPC.Port))

	// gRPC
	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%s", config.Options.GRPC.Host, config.Options.GRPC.Port))
	if err != nil {
		log.Z.Fatal("failed to listen.", zap.Error(err))
	}

	grpcServer := grpc.NewServer(config.Options.GRPC.ServerOptions...)

	service.InitServices()

	pb.RegisterInstanceServiceServer(grpcServer, service.InstanceServer)
	pb.RegisterUserServiceServer(grpcServer, service.UserServer)
	pb.RegisterUtilityServiceServer(grpcServer, service.UtilityServer)
	pb.RegisterReminderServiceServer(grpcServer, service.ReminderServer)
	pb.RegisterAnalyticsServiceServer(grpcServer, service.AnalyticsServer)
	pb.RegisterEntertainmentServiceServer(grpcServer, service.EntertainmentServer)
	pb.RegisterReverseServiceServer(grpcServer, service.ReverseServer)

	// Parallel cron jobs
	go cron.RunCronJobs()

	if config.AppEnvironment == enum.DEVELOPMENT {
		// Register reflection service on gRPC server.
		reflection.Register(grpcServer)
	}

	if err := grpcServer.Serve(lis); err != nil {
		log.Z.Fatal("failed to serve.", zap.Error(err))
		return
	}
}
