package config

import "testing"

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
		// Not normalised: the string goes straight to the blob store.
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
