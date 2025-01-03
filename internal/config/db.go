package config

import (
	"os"
)

type DBOptions struct {
	Name       string
	Migrations bool
}

func dbName() string {
	value := os.Getenv("GINBOT_DB_NAME")
	if value == "" {
		return "mangatsu"
	}
	return value
}

func dbMigrationsEnabled() bool {
	// Disabled only when explicitly set to false.
	return os.Getenv("GINBOT_DB_MIGRATIONS") != "false"
}
