package log

import (
	"log"

	"github.com/lasikuu/GinBot/pkg/enum"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Z is the structured logger and the default choice.
var Z *zap.Logger

// S is the sugared logger.
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

	S = Z.Sugar()
}

// Sync must be deferred from main, not from InitializeLogger, which would flush
// immediately and leave the rest of the process unflushed.
func Sync() {
	if Z == nil {
		return
	}

	// Sync on stderr/stdout returns EINVAL or ENOTTY and is not worth surfacing.
	_ = Z.Sync()
}
