package main

import (
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/cron"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/matrix"
	"go.uber.org/zap"
)

func main() {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	config.SetEnv()

	log.Z.Info("starting GinBot client for Matrix.", zap.String("host", config.Options.GRPC.Host), zap.String("port", config.Options.GRPC.Port))

	// gRPC client for Matrix
	matrix.NewMatrixClient()

	// Matrix client
	matrix.InitializeMatrix()

	// Parallel cron jobs
	go cron.RunCronJobs()
}
