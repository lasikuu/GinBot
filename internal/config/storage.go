package config

import "os"

// StorageOptions configures where blobs are written.
type StorageOptions struct {
	Path string
}

// storagePath returns the directory blobs are written under.
//
// The default is relative to the working directory, exactly like
// internal/auth's DefaultCertsDir: binaries must therefore be launched from
// the repository root for the default to resolve where expected.
func storagePath() string {
	value := os.Getenv("GINBOT_STORAGE_PATH")
	if value == "" {
		return "./storage"
	}
	return value
}
