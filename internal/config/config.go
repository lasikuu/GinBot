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
	Matrix  MatrixOptions
	Discord DiscordOptions
	DB      DBOptions
	GRPC    GRPCServerOptions
	Storage StorageOptions
	Repost  RepostOptions
}

var AppEnvironment enum.Environment
var LogLevel zapcore.Level

// Options stores the global configuration for the server
var Options *OptionsModel

// LoadEnv loads the environment variables.
func LoadEnv() {
	var err = godotenv.Load()
	if err != nil {
		fmt.Println("error loading environment vars:", err)
	}

	loadEnvironment()
	loadLogLevel()
}

// SetEnv sets the environment variables into Options and Credentials
func SetEnv() {
	// GINBOT_WEB_URL is deliberately NOT carried on OptionsModel. Nothing reads
	// the raw URL; the only thing derived from it is the repost exclusion
	// below, and an exported field that is written and never read is invisible
	// to `unused` and outlives the reason it was added. Add it back when
	// something needs the URL itself.
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

// webURL returns the bot's own public web address, raw and unnormalised.
//
// It stays raw on purpose: hostOf normalises it at the point of use, and
// trimming or lowercasing here as well would give the two layers different
// ideas of what was configured. Empty means the bot has no web presence.
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
