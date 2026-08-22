package main

import (
	"context"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/matrix"
	"go.uber.org/zap"
)

func main() {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	defer log.Sync()
	config.SetEnv()

	log.Z.Info("starting GinBot client for Matrix.", zap.String("host", config.Options.GRPC.Host), zap.String("port", config.Options.GRPC.Port))

	// The action stream lives as long as the process.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// gRPC client for Matrix
	matrix.NewMatrixClient(ctx)

	// Matrix client
	matrix.InitializeMatrix()
}
