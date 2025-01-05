package log

import (
	"log"

	"github.com/lasikuu/GinBot/pkg/enum"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Z *zap.Logger
var S *zap.SugaredLogger

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
