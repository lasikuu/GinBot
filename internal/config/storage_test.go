package config

import "testing"

// Tests for internal/config/storage.go. TestMain and unsetEnv come from
// repost_test.go.

func TestStoragePath(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  string
	}{
		{"unset defaults to ./storage", false, "", "./storage"},
		{"an empty value defaults to ./storage", true, "", "./storage"},
		{"an absolute path is used as given", true, "/var/lib/ginbot/storage", "/var/lib/ginbot/storage"},
		// Not normalised: the accessor hands the string straight to the blob
		// store, so a trailing slash or a relative path is the operator's to
		// get right, and pinning that here stops a well-meant filepath.Clean
		// from being added without a decision.
		{"a relative path is not normalised", true, "blobs/", "blobs/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("GINBOT_STORAGE_PATH", tt.value)
			} else {
				unsetEnv(t, "GINBOT_STORAGE_PATH")
			}

			if got := storagePath(); got != tt.want {
				t.Errorf("storagePath() = %q, want %q", got, tt.want)
			}
		})
	}
}
