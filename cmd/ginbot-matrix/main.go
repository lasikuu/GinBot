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

	// Connect service clients first: a command handler and an action handler
	// both need them, and neither runs yet.
	clients, err := matrix.NewMatrixClient(ctx)
	if err != nil {
		log.Z.Fatal("failed to connect to ginbot-server.", zap.Error(err))
	}
	defer clients.Close()

	// Blocks until shutdown. The reverse action stream is started from inside,
	// after the Matrix client has been assigned — an action handler may read it.
	matrix.InitializeMatrix(ctx, clients)
}
