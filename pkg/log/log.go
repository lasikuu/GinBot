package log

import (
	"log"

	"github.com/lasikuu/GinBot/pkg/enum"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Z provides high performance logging. Normally, you would use this.
var Z *zap.Logger

// S provides a sugared logger. This is useful for advanced logging.
var S *zap.SugaredLogger

// InitializeLogger initializes the logger with the given environment and log level.
func InitializeLogger(env enum.Environment, logLevel zapcore.Level) {
	var loggerErr error
	if env == enum.PRODUCTION {
		Z, loggerErr = zap.NewProduction(zap.IncreaseLevel(logLevel))
	} else {
		Z, loggerErr = zap.NewDevelopment(zap.IncreaseLevel(logLevel))
	}

	if loggerErr != nil {
		log.Fatal("Failed to initialize zap logger: ", loggerErr)
	}

	defer Z.Sync()
	S = Z.Sugar()
}
