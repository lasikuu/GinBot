package main

import (
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

func main() {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	config.SetEnv()
	// db.EnsureLatestVersion()

	log.Z.Info("starting GinBot.", zap.String("foo", "bar"))
}
