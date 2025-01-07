package main

import (
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/discord"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

func main() {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	config.SetEnv()

	log.Z.Info("starting GinBot client for Discord.", zap.String("host", config.Options.GRPC.Host), zap.String("port", config.Options.GRPC.Port))

	// gRPC client for Discord
	discord.NewDiscordClient()

	// Discord client
	discord.InitializeDiscord()
}
