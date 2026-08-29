package main

import (
	"context"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/discord"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

func main() {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	defer log.Sync()
	config.SetEnv()

	log.Z.Info("starting GinBot client for Discord.", zap.String("host", config.Options.GRPC.Host), zap.String("port", config.Options.GRPC.Port))

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	clients, err := discord.NewDiscordClient(ctx)
	if err != nil {
		log.Z.Fatal("failed to connect to ginbot-server.", zap.Error(err))
	}
	defer clients.Close()

	// Blocks until shutdown; starts the reverse action stream from inside.
	discord.InitializeDiscord(ctx, clients)
}
