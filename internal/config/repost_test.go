package config

import (
	"os"
	"testing"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// TestMain gives this package a logger: repostIntThreshold and
// repostMinEntropy log a warning through log.Z on a malformed value, and log.Z
// is nil until something initialises it.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	os.Exit(m.Run())
}

// unsetEnv removes a variable entirely, so a test can exercise "not set at
// all" rather than "set to empty string" — t.Setenv can only do the latter.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	// Snapshot and restore, matching t.Setenv's own cleanup contract, so the
	// unset does not leak into another test.
	if value, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() {
			if err := os.Setenv(key, value); err != nil {
				t.Errorf("restore %s: %v", key, err)
			}
		})
	} else {
		t.Cleanup(func() {
			if err := os.Unsetenv(key); err != nil {
				t.Errorf("re-unset %s: %v", key, err)
			}
		})
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

// ── repostEnabled ─────────────────────────────────────────────────────────────

func TestRepostEnabled(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{"unset defaults to disabled", false, "", false},
		{"true enables it", true, "true", true},
		{"false stays disabled", true, "false", false},
		{"any other value stays disabled", true, "1", false},
		{"empty value stays disabled", true, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("GINBOT_REPOST", tt.value)
			} else {
				unsetEnv(t, "GINBOT_REPOST")
			}

			if got := repostEnabled(); got != tt.want {
				t.Errorf("repostEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ── Integer thresholds: default, valid, malformed-falls-back ────────────────

// intThresholdCase names one (accessor, env var, default) triple, so the
// table below cannot silently miss checking that a given accessor reads the
// env var it is documented to.
type intThresholdCase struct {
	name     string
	accessor func() int
	envVar   string
	def      int
}

func intThresholdCases() []intThresholdCase {
	return []intThresholdCase{
		{"TierIdentical", repostTierIdentical, "GINBOT_REPOST_TIER_IDENTICAL", 0},
		{"TierHigh", repostTierHigh, "GINBOT_REPOST_TIER_HIGH", 3},
		{"TierProbable", repostTierProbable, "GINBOT_REPOST_TIER_PROBABLE", 7},
		{"MinWidth", repostMinWidth, "GINBOT_REPOST_MIN_WIDTH", 128},
		{"MinHeight", repostMinHeight, "GINBOT_REPOST_MIN_HEIGHT", 128},
	}
}

func TestRepostIntThresholdsDefaultWhenUnset(t *testing.T) {
	for _, tc := range intThresholdCases() {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, tc.envVar)

			if got := tc.accessor(); got != tc.def {
				t.Errorf("%s() = %d, want the default %d", tc.name, got, tc.def)
			}
		})
	}
}

func TestRepostIntThresholdsReadAValidValue(t *testing.T) {
	for _, tc := range intThresholdCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envVar, "42")

			if got := tc.accessor(); got != 42 {
				t.Errorf("%s() = %d, want 42", tc.name, got)
			}
		})
	}
}

// TestRepostIntThresholdsFallBackOnAMalformedValue: a tuning knob must never
// fail startup over a bad value (internal/config/repost.go's own stated
// contract) — it warns and falls back to the default instead.
func TestRepostIntThresholdsFallBackOnAMalformedValue(t *testing.T) {
	for _, tc := range intThresholdCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envVar, "not-a-number")

			if got := tc.accessor(); got != tc.def {
				t.Errorf("%s() = %d for a malformed value, want the default %d", tc.name, got, tc.def)
			}
		})
	}
}

func TestRepostIntThresholdsAcceptANegativeValue(t *testing.T) {
	// Identical's floor is 0 by default, but a caller might legitimately
	// widen or narrow bands with a negative-then-clamped value at the
	// repost.Tiers.Normalise layer; the config accessor itself must not
	// second-guess strconv.Atoi's own valid parse.
	t.Setenv("GINBOT_REPOST_TIER_IDENTICAL", "-1")

	if got := repostTierIdentical(); got != -1 {
		t.Errorf("repostTierIdentical() = %d, want -1 (parsed as-is; clamping is repost.Tiers.Normalise's job)", got)
	}
}

// ── repostMinEntropy ──────────────────────────────────────────────────────────

func TestRepostMinEntropyDefaultsWhenUnset(t *testing.T) {
	unsetEnv(t, "GINBOT_REPOST_MIN_ENTROPY")

	if got := repostMinEntropy(); got != 3.0 {
		t.Errorf("repostMinEntropy() = %v, want 3.0", got)
	}
}

func TestRepostMinEntropyReadsAValidValue(t *testing.T) {
	t.Setenv("GINBOT_REPOST_MIN_ENTROPY", "4.5")

	if got := repostMinEntropy(); got != 4.5 {
		t.Errorf("repostMinEntropy() = %v, want 4.5", got)
	}
}

func TestRepostMinEntropyFallsBackOnAMalformedValue(t *testing.T) {
	t.Setenv("GINBOT_REPOST_MIN_ENTROPY", "not-a-float")

	if got := repostMinEntropy(); got != 3.0 {
		t.Errorf("repostMinEntropy() = %v for a malformed value, want the default 3.0", got)
	}
}

// ── repostExcludedHosts ───────────────────────────────────────────────────────

func TestRepostExcludedHosts(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  []string
	}{
		{"unset yields no exclusions", false, "", nil},
		{"empty value yields no exclusions", true, "", nil},
		{"a single host", true, "bot.example", []string{"bot.example"}},
		{"multiple hosts", true, "bot.example,cdn.example", []string{"bot.example", "cdn.example"}},
		{"empty elements are dropped", true, "bot.example,,cdn.example,", []string{"bot.example", "cdn.example"}},
		{"whitespace around a host is trimmed", true, " bot.example , cdn.example ", []string{"bot.example", "cdn.example"}},
		{"whitespace-only elements are dropped", true, "  ,\t,", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("GINBOT_REPOST_EXCLUDED_HOSTS", tt.value)
			} else {
				unsetEnv(t, "GINBOT_REPOST_EXCLUDED_HOSTS")
			}

			got := repostExcludedHosts()
			if len(got) != len(tt.want) {
				t.Fatalf("repostExcludedHosts() = %q, want %q", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("repostExcludedHosts()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ── repostFFmpegPath ──────────────────────────────────────────────────────────

func TestRepostFFmpegPath(t *testing.T) {
	unsetEnv(t, "GINBOT_REPOST_FFMPEG_PATH")
	if got := repostFFmpegPath(); got != "" {
		t.Errorf("repostFFmpegPath() = %q, want empty when unset (LookupFFmpeg falls back to PATH)", got)
	}

	t.Setenv("GINBOT_REPOST_FFMPEG_PATH", "/usr/local/bin/ffmpeg")
	if got := repostFFmpegPath(); got != "/usr/local/bin/ffmpeg" {
		t.Errorf("repostFFmpegPath() = %q, want %q", got, "/usr/local/bin/ffmpeg")
	}
}
