package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/lasikuu/GinBot/pkg/enum"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type OptionsModel struct {
	Discord DiscordOptions
	DB      DBOptions
}

type CredentialsModel struct {
	JWTSecret  string
	Passphrase string
}

var AppEnvironment enum.Environment
var LogLevel zapcore.Level

// Options stores the global configuration for the server
var Options *OptionsModel

// LoadEnv loads the environment variables.
func LoadEnv() {
	var err = godotenv.Load()
	if err != nil {
		fmt.Println("No .env file or environmentals found.")
	}

	loadEnvironment()
	loadLogLevel()
}

// SetEnv sets the environment variables into Options and Credentials
func SetEnv() {
	Options = &OptionsModel{
		Discord: DiscordOptions{
			OwnerId:  ownerId(),
			BotToken: botToken(),
			ClientId: clientId(),
		},
		DB: DBOptions{
			Name:       dbName(),
			Migrations: dbMigrationsEnabled(),
		},
	}
}

func loadEnvironment() {
	value := os.Getenv("GINBOT_ENV")
	if value == "production" {
		AppEnvironment = enum.Production
		return
	}
	AppEnvironment = enum.Development
}

func loadLogLevel() {
	value := os.Getenv("GINBOT_LOG_LEVEL")
	switch value {
	case "debug":
		LogLevel = zap.DebugLevel
	case "warn":
		LogLevel = zap.WarnLevel
	case "error":
		LogLevel = zap.ErrorLevel
	default:
		LogLevel = zap.InfoLevel
	}
}
