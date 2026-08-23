package config

import (
	"math"
	"strconv"
	"testing"
)

// Tests for internal/config/db.go. TestMain and unsetEnv are declared in
// repost_test.go and are reused here rather than redeclared.

// ── String accessors: default when unset, verbatim when set ─────────────────

// dbStringCase names one (accessor, env var, default) triple, so the table
// cannot silently stop checking that an accessor reads the variable it is
// documented to — the same construction intThresholdCases uses in
// repost_test.go.
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

// TestDBStringAccessorsDefaultOnAnEmptyValue: these accessors treat "" as
// "not configured" rather than as a deliberate empty value, which matters
// because an empty database name or username produces a connection URI that
// fails much later, at InitDB, with a far less obvious message.
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

// ── dbPort ──────────────────────────────────────────────────────────────────

// TestDBPort covers every branch of the ParseInt: unset, valid, empty,
// unparseable, and — the one worth pinning — a value that parses as an
// integer but does not fit in int32. ParseInt is given a bitSize of 32, so
// that case is a parse ERROR rather than a silent truncation to a wrong port,
// and the accessor must fall back rather than dial port -1.
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

// ── dbMigrationsEnabled ─────────────────────────────────────────────────────

// TestDBMigrationsEnabled pins the surprising part of the contract: the check
// is `!= "false"`, so ONLY the exact lowercase literal disables migrations.
// "FALSE", "0" and "no" all leave them ON. That is a boot-time behaviour a
// deployment can get wrong silently — the server runs goose.Up against a
// database the operator believed it would not touch — so the exact-match rule
// is asserted rather than left to be inferred from the comment on the
// accessor.
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
