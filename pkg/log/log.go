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

	S = Z.Sugar()
}

// Sync flushes any buffered log entries. Callers should defer this from main,
// not from InitializeLogger — deferring it there would flush immediately and
// leave the rest of the process lifetime unflushed.
func Sync() {
	if Z == nil {
		return
	}

	// Sync on stderr/stdout returns EINVAL or ENOTTY on Linux and macOS.
	// There is nothing useful to do about it, and it is not an error worth surfacing.
	_ = Z.Sync()
}
