package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/cron"
	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/enum"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
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
	defer log.Sync()
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

	validationInterceptor, err := interceptor.NewValidationUnaryInterceptor()
	if err != nil {
		log.Z.Fatal("failed to build validation interceptor.", zap.Error(err))
	}

	serverOptions := append(
		config.ServerOptions(),
		grpc.ChainUnaryInterceptor(validationInterceptor),
	)
	grpcServer := grpc.NewServer(serverOptions...)

	service.InitServices()

	pb.RegisterInstanceServiceServer(grpcServer, service.InstanceServer)
	pb.RegisterUserServiceServer(grpcServer, service.UserServer)
	pb.RegisterUtilityServiceServer(grpcServer, service.UtilityServer)
	pb.RegisterReminderServiceServer(grpcServer, service.ReminderServer)
	pb.RegisterAnalyticsServiceServer(grpcServer, service.AnalyticsServer)
	pb.RegisterEntertainmentServiceServer(grpcServer, service.EntertainmentServer)
	pb.RegisterReverseServiceServer(grpcServer, service.ReverseServer)

	// Cancelled on SIGINT or SIGTERM. Serve() is run in a goroutine so the signal
	// can be acted on: without this, main blocked in Serve() until log.Z.Fatal
	// called os.Exit, which skips every deferred call — so log.Sync, db.CloseDB
	// and the cron cancellation above could never actually run.
	shutdownCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// Parallel cron jobs
	go cron.RunCronJobs(shutdownCtx)

	if config.AppEnvironment == enum.DEVELOPMENT {
		// Register reflection service on gRPC server.
		reflection.Register(grpcServer)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- grpcServer.Serve(lis)
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			log.Z.Error("gRPC server stopped.", zap.Error(err))
		}
	case <-shutdownCtx.Done():
		log.Z.Info("shutdown signal received, draining connections.")
		grpcServer.GracefulStop()
	}

	log.Z.Info("gracefully shutting down.")
}
