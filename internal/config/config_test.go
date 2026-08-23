package config

import (
	"testing"

	"github.com/lasikuu/GinBot/pkg/enum"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Tests for internal/config/config.go. TestMain and unsetEnv come from
// repost_test.go.

// restoreGlobals snapshots AppEnvironment and LogLevel and puts them back
// afterwards. loadEnvironment and loadLogLevel write package-level state
// rather than returning a value, so without this a test would leave the
// package configured for whatever it happened to assert last and the suite's
// result would depend on test ordering.
func restoreGlobals(t *testing.T) {
	t.Helper()

	environment, logLevel := AppEnvironment, LogLevel
	t.Cleanup(func() {
		AppEnvironment, LogLevel = environment, logLevel
	})
}

// TestLoadEnvironmentTreatsOnlyTheExactLiteralProductionAsProduction.
//
// Getting this wrong fails OPEN, which is why it is worth an exact-match
// table: development mode enables gRPC server reflection (cmd/ginbot-server),
// makes the cron loop fan out CLIENT_ACTION_SEND_TEST every minute, and
// switches zap to the development encoder. A production deployment that spells
// the value "Production" silently gets all three.
func TestLoadEnvironmentTreatsOnlyTheExactLiteralProductionAsProduction(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  enum.Environment
	}{
		{"the exact lowercase production", true, "production", enum.PRODUCTION},
		{"capitalised Production is development", true, "Production", enum.DEVELOPMENT},
		{"uppercase PRODUCTION is development", true, "PRODUCTION", enum.DEVELOPMENT},
		{"the abbreviation prod is development", true, "prod", enum.DEVELOPMENT},
		{"an empty value is development", true, "", enum.DEVELOPMENT},
		{"unset is development", false, "", enum.DEVELOPMENT},
		{"garbage is development", true, "banana", enum.DEVELOPMENT},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreGlobals(t)

			if tt.set {
				t.Setenv("GINBOT_ENV", tt.value)
			} else {
				unsetEnv(t, "GINBOT_ENV")
			}

			loadEnvironment()

			if AppEnvironment != tt.want {
				t.Errorf("AppEnvironment = %v, want %v", AppEnvironment, tt.want)
			}
		})
	}
}

// TestLoadLogLevelFallsBackToInfo covers every arm of the switch, including
// the fact that there is deliberately NO "info" case — info is what the
// default arm produces, so "info", "", an unset variable and outright garbage
// all land on the same level. A future refactor that adds an explicit "info"
// case and changes the default to, say, Debug would leak request payloads
// into production logs for every operator who never set the variable.
func TestLoadLogLevelFallsBackToInfo(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  zapcore.Level
	}{
		{"debug", true, "debug", zap.DebugLevel},
		{"warn", true, "warn", zap.WarnLevel},
		{"error", true, "error", zap.ErrorLevel},
		{"info reaches the default arm", true, "info", zap.InfoLevel},
		{"an empty value reaches the default arm", true, "", zap.InfoLevel},
		{"unset reaches the default arm", false, "", zap.InfoLevel},
		{"garbage reaches the default arm", true, "verbose", zap.InfoLevel},
		// The switch matches exact strings, so case matters here too.
		{"uppercase DEBUG reaches the default arm", true, "DEBUG", zap.InfoLevel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreGlobals(t)

			if tt.set {
				t.Setenv("GINBOT_LOG_LEVEL", tt.value)
			} else {
				unsetEnv(t, "GINBOT_LOG_LEVEL")
			}

			loadLogLevel()

			if LogLevel != tt.want {
				t.Errorf("LogLevel = %v, want %v", LogLevel, tt.want)
			}
		})
	}
}

// ── webURL ───────────────────────────────────────────────────────────────────

// TestWebURL: a raw passthrough with no default. It is normalised by hostOf
// at the point of use, not here, so this must not trim or lowercase — doing so
// would make the two layers disagree about what was configured.
func TestWebURL(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  string
	}{
		{"unset yields the empty string", false, "", ""},
		{"an empty value yields the empty string", true, "", ""},
		{"a full URL is passed through", true, "https://bot.example/", "https://bot.example/"},
		{"a bare host is passed through", true, "bot.example", "bot.example"},
		{"surrounding whitespace is NOT trimmed here", true, "  https://bot.example/  ", "  https://bot.example/  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("GINBOT_WEB_URL", tt.value)
			} else {
				unsetEnv(t, "GINBOT_WEB_URL")
			}

			if got := webURL(); got != tt.want {
				t.Errorf("webURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
