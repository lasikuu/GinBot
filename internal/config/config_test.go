package config

import (
	"testing"

	"github.com/lasikuu/GinBot/pkg/enum"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// restoreGlobals keeps the suite order-independent: the loaders write
// package-level state rather than returning a value.
func restoreGlobals(t *testing.T) {
	t.Helper()

	environment, logLevel := AppEnvironment, LogLevel
	t.Cleanup(func() {
		AppEnvironment, LogLevel = environment, logLevel
	})
}

// Fails open: "Production" silently gets reflection and the cron test fan-out.
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

// No "info" case: a default of Debug would leak payloads for anyone unset.
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

// Raw passthrough: hostOf normalises at the point of use instead.
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
