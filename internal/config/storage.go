package config

import "os"

type StorageOptions struct {
	Path string
}

// The default is relative to the working directory, so a binary must be
// launched from the repository root.
func storagePath() string {
	value := os.Getenv("GINBOT_STORAGE_PATH")
	if value == "" {
		return "./storage"
	}
	return value
}
