package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	// Embeds the IANA zone database in the binary so that SetTimezone can
	// resolve a zone on a host that ships none, such as a scratch container.
	// Without it time.LoadLocation fails for every name and no timezone is
	// settable at all.
	_ "time/tzdata"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/cron"
	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/enum"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/storage"
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

	validationUnary, err := interceptor.NewValidationUnaryInterceptor()
	if err != nil {
		log.Z.Fatal("failed to build validation interceptor.", zap.Error(err))
	}

	validationStream, err := interceptor.NewValidationStreamInterceptor()
	if err != nil {
		log.Z.Fatal("failed to build stream validation interceptor.", zap.Error(err))
	}

	// Order matters, outermost first:
	//
	//   - recovery, so a panic anywhere below it fails one call instead of the
	//     process;
	//   - validation, so a malformed request is rejected before it costs the
	//     database round trip that resolving a caller needs;
	//   - clearance, which resolves the caller and enforces the per-RPC minimum;
	//   - origin, innermost, because it deliberately does nothing unless
	//     clearance put a caller in the context. Public methods resolve nobody,
	//     and their position in the chain is not what stops them writing rows —
	//     the caller check inside the origin interceptor is.
	serverOptions := append(
		config.ServerOptions(),
		grpc.ChainUnaryInterceptor(
			interceptor.RecoverUnaryInterceptor,
			validationUnary,
			interceptor.NewClearanceUnaryInterceptor(interceptor.DefaultRequirements(), db.GetUserByPlatformUID),
			interceptor.NewOriginUnaryInterceptor(db.GetOrCreateDestinationByMeta),
		),
		grpc.ChainStreamInterceptor(
			interceptor.RecoverStreamInterceptor,
			validationStream,
		),
	)
	grpcServer := grpc.NewServer(serverOptions...)

	// Trigger media needs somewhere to write blobs before TriggerServer is
	// constructed: NewTriggerServer reads the package-level store via
	// storage.Default(), which is nil until this call.
	if err := storage.Init(config.Options.Storage.Path); err != nil {
		log.Z.Fatal("failed to initialize storage.", zap.Error(err))
	}

	service.InitServices()

	pb.RegisterInstanceServiceServer(grpcServer, service.InstanceServer)
	pb.RegisterUserServiceServer(grpcServer, service.UserServer)
	pb.RegisterUtilityServiceServer(grpcServer, service.UtilityServer)
	pb.RegisterReminderServiceServer(grpcServer, service.ReminderServer)
	pb.RegisterAnalyticsServiceServer(grpcServer, service.AnalyticsServer)
	pb.RegisterEntertainmentServiceServer(grpcServer, service.EntertainmentServer)
	pb.RegisterReverseServiceServer(grpcServer, service.ReverseServer)
	pb.RegisterTriggerServiceServer(grpcServer, service.TriggerServer)
	pb.RegisterRepostServiceServer(grpcServer, service.RepostServer)

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
