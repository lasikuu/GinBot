package config

import (
	"math"
	"strconv"
	"testing"
)

// One (accessor, env var, default) triple, so the table cannot stop checking
// that an accessor reads the variable it documents.
type dbStringCase struct {
	name     string
	accessor func() string
	envVar   string
	def      string
}

func dbStringCases() []dbStringCase {
	return []dbStringCase{
		{"dbHost", dbHost, "GINBOT_DB_HOST", "localhost"},
		{"dbUsername", dbUsername, "GINBOT_DB_USERNAME", "ginbot"},
		{"dbPassword", dbPassword, "GINBOT_DB_PASSWORD", "gin123"},
		{"dbName", dbName, "GINBOT_DB_NAME", "ginbot"},
	}
}

func TestDBStringAccessorsDefaultWhenUnset(t *testing.T) {
	for _, tc := range dbStringCases() {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, tc.envVar)

			if got := tc.accessor(); got != tc.def {
				t.Errorf("%s() = %q, want the default %q", tc.name, got, tc.def)
			}
		})
	}
}

// "" means "not configured"; an empty name would fail much later at InitDB.
func TestDBStringAccessorsDefaultOnAnEmptyValue(t *testing.T) {
	for _, tc := range dbStringCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envVar, "")

			if got := tc.accessor(); got != tc.def {
				t.Errorf("%s() = %q for an empty value, want the default %q", tc.name, got, tc.def)
			}
		})
	}
}

func TestDBStringAccessorsReadAnOverride(t *testing.T) {
	for _, tc := range dbStringCases() {
		t.Run(tc.name, func(t *testing.T) {
			const override = "configured-value"
			t.Setenv(tc.envVar, override)

			if got := tc.accessor(); got != override {
				t.Errorf("%s() = %q, want %q", tc.name, got, override)
			}
		})
	}
}

// A bitSize of 32 makes an out-of-range value an error, not a truncation.
func TestDBPort(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  int32
	}{
		{"unset defaults to 5432", false, "", 5432},
		{"a valid port is read", true, "6543", 6543},
		{"an empty value defaults", true, "", 5432},
		{"a non-numeric value warns and defaults", true, "not-a-port", 5432},
		{"a value past int32 warns and defaults", true, strconv.FormatInt(math.MaxInt32+1, 10), 5432},
		{"a negative value is parsed as-is", true, "-1", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("GINBOT_DB_PORT", tt.value)
			} else {
				unsetEnv(t, "GINBOT_DB_PORT")
			}

			if got := dbPort(); got != tt.want {
				t.Errorf("dbPort() = %d, want %d", got, tt.want)
			}
		})
	}
}

// `!= "false"`, so "FALSE", "0" and "no" all leave goose.Up running.
func TestDBMigrationsEnabled(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{"unset leaves migrations on", false, "", true},
		{"the exact lowercase false disables them", true, "false", false},
		{"uppercase FALSE does NOT disable them", true, "FALSE", true},
		{"mixed-case False does NOT disable them", true, "False", true},
		{"zero does NOT disable them", true, "0", true},
		{"no does NOT disable them", true, "no", true},
		{"true leaves them on", true, "true", true},
		{"an empty value leaves them on", true, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("GINBOT_DB_MIGRATIONS", tt.value)
			} else {
				unsetEnv(t, "GINBOT_DB_MIGRATIONS")
			}

			if got := dbMigrationsEnabled(); got != tt.want {
				t.Errorf("dbMigrationsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
