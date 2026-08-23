package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/lasikuu/GinBot/internal/auth"
	"github.com/lasikuu/GinBot/pkg/enum"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type OptionsModel struct {
	Matrix  MatrixOptions
	Discord DiscordOptions
	DB      DBOptions
	GRPC    GRPCServerOptions
	Storage StorageOptions
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
	// Loaded once and shared: building it per client parsed the same key pair twice.
	clientOptions := GRPCClientOptions{
		DialOptions: dialOptions(),
	}

	Options = &OptionsModel{
		Matrix: MatrixOptions{
			GRPCClientOptions: clientOptions,
			HomeServerURL:     homeServerUrl(),
			AccessToken:       accessToken(),
			UserID:            userId(),
		},
		Discord: DiscordOptions{
			GRPCClientOptions: clientOptions,
			OwnerId:           ownerId(),
			BotToken:          botToken(),
			ClientId:          clientId(),
			EraseCommands:     eraseCommands(),
			CommandPrefixes:   commandPrefixes(),
			MessageContent:    messageContent(),
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
	}
}

func dialOptions() []grpc.DialOption {
	gRPCDialOptions := []grpc.DialOption{
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxGRPCMessageBytes),
			grpc.MaxCallSendMsgSize(MaxGRPCMessageBytes),
		),
	}

	if !gRPCTLS() {
		gRPCDialOptions = append(gRPCDialOptions, grpc.WithTransportCredentials(insecure.NewCredentials()))
		return gRPCDialOptions
	}

	tlsCredentials := auth.LoadClientCredentials(certsPath())

	gRPCDialOptions = append(gRPCDialOptions, grpc.WithTransportCredentials(tlsCredentials))
	return gRPCDialOptions
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
