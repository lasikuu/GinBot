package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/joho/godotenv"
	"github.com/lasikuu/GinBot/pkg/enum"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type OptionsModel struct {
	Matrix  MatrixOptions
	Discord DiscordOptions
	DB      DBOptions
	GRPC    GRPCServerOptions
	Storage StorageOptions
	Repost  RepostOptions
}

var AppEnvironment enum.Environment
var LogLevel zapcore.Level

// Options is nil until SetEnv runs.
var Options *OptionsModel

// loadEnvReport prefixes the only output LoadEnv produces. Printed, not logged:
// InitializeLogger has not run yet.
const loadEnvReport = "error loading environment vars:"

func LoadEnv() {
	// Containers carry no .env and configure from the real environment, so only
	// a present-but-unreadable file is worth reporting.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Println(loadEnvReport, err)
	}

	loadEnvironment()
	loadLogLevel()
}

func SetEnv() {
	// Not on OptionsModel: only the repost exclusion below derives from it.
	web := webURL()

	Options = &OptionsModel{
		Matrix: MatrixOptions{
			HomeServerURL: homeServerUrl(),
			AccessToken:   accessToken(),
			UserID:        userId(),
		},
		Discord: DiscordOptions{
			OwnerId:         ownerId(),
			BotToken:        botToken(),
			ClientId:        clientId(),
			EraseCommands:   eraseCommands(),
			CommandPrefixes: commandPrefixes(),
			MessageContent:  messageContent(),
		},
		GRPC: GRPCServerOptions{
			Host:      gRPCHost(),
			Port:      gRPCPort(),
			TLS:       gRPCTLS(),
			CertsPath: certsPath(),
		},
		DB: DBOptions{
			Host:       dbHost(),
			Port:       dbPort(),
			Name:       dbName(),
			Username:   dbUsername(),
			Password:   dbPassword(),
			Migrations: dbMigrationsEnabled(),
		},
		Storage: StorageOptions{
			Path: storagePath(),
		},
		Repost: RepostOptions{
			Enabled:       repostEnabled(),
			TierIdentical: repostTierIdentical(),
			TierHigh:      repostTierHigh(),
			TierProbable:  repostTierProbable(),
			MinWidth:      repostMinWidth(),
			MinHeight:     repostMinHeight(),
			MinEntropy:    repostMinEntropy(),
			ExcludedHosts: withSelfHost(repostExcludedHosts(), web),
			FFmpegPath:    repostFFmpegPath(),
		},
	}
}

// webURL is unnormalised; hostOf normalises it at the point of use.
func webURL() string {
	return os.Getenv("GINBOT_WEB_URL")
}

func loadEnvironment() {
	value := os.Getenv("GINBOT_ENV")
	if value == "production" {
		AppEnvironment = enum.PRODUCTION
		return
	}
	AppEnvironment = enum.DEVELOPMENT
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
