package config

import (
	"os"
	"strconv"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

type DBOptions struct {
	Host       string
	Port       int32
	Name       string
	Username   string
	Password   string
	Migrations bool
}

func dbHost() string {
	value := os.Getenv("GINBOT_DB_HOST")
	if value == "" {
		return "localhost"
	}
	return value
}

func dbPort() int32 {
	value := os.Getenv("GINBOT_DB_PORT")
	if value == "" {
		return 5432
	}

	intValue, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		log.Z.Warn("failed to parse db port. defaulting to 5432.", zap.Error(err))
		return 5432
	}

	return int32(intValue)
}

func dbUsername() string {
	value := os.Getenv("GINBOT_DB_USERNAME")
	if value == "" {
		return "ginbot"
	}
	return value
}

func dbPassword() string {
	value := os.Getenv("GINBOT_DB_PASSWORD")
	if value == "" {
		return "gin123"
	}
	return value
}

func dbName() string {
	value := os.Getenv("GINBOT_DB_NAME")
	if value == "" {
		return "ginbot"
	}
	return value
}

func dbMigrationsEnabled() bool {
	// Disabled only when explicitly set to false.
	return os.Getenv("GINBOT_DB_MIGRATIONS") != "false"
}
