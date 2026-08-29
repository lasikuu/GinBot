package config

import (
	"os"
	"testing"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// log.Z is nil until initialised, and the accessors warn through it.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	os.Exit(m.Run())
}

// unsetEnv exercises "not set at all"; t.Setenv can only set an empty string.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	// Restore on cleanup, matching t.Setenv, so the unset does not leak.
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

// One triple per accessor, so the table cannot stop checking its env var.
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
	// Clamping happens in repost.Tiers.Normalise, not here.
	t.Setenv("GINBOT_REPOST_TIER_IDENTICAL", "-1")

	if got := repostTierIdentical(); got != -1 {
		t.Errorf("repostTierIdentical() = %d, want -1 (parsed as-is; clamping is repost.Tiers.Normalise's job)", got)
	}
}

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

func TestHostOfExtractsABareLowercaseHost(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"https URL with a path and query", "https://bot.example/x?y=1", "bot.example"},
		{"http URL with a port", "http://bot.example:8080", "bot.example"},
		{"a bare host with no scheme", "bot.example", "bot.example"},
		{"a bare host with a path and no scheme", "bot.example/path", "bot.example"},
		{"surrounding whitespace and mixed case", "  https://Bot.Example/  ", "bot.example"},
		{"uppercase bare host with a port", "BOT.EXAMPLE:443", "bot.example"},
		{"a scheme with no host at all", "://", ""},
		{"a path with no host", "/just/a/path", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hostOf(tt.raw); got != tt.want {
				t.Errorf("hostOf(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestHostOfDoesNotStripAWWWPrefix(t *testing.T) {
	if got := hostOf("https://www.bot.example/"); got != "www.bot.example" {
		t.Errorf("hostOf(%q) = %q, want %q (urlnorm.New owns www. stripping)",
			"https://www.bot.example/", got, "www.bot.example")
	}
}

func TestWithSelfHostAppendsTheSelfHostLast(t *testing.T) {
	got := withSelfHost([]string{"cdn.example"}, "https://bot.example/")

	want := []string{"cdn.example", "bot.example"}
	if len(got) != len(want) {
		t.Fatalf("withSelfHost() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("withSelfHost()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// An empty exclusion entry could match everything downstream.
func TestWithSelfHostSkipsAnEmptySelfHost(t *testing.T) {
	tests := []struct {
		name    string
		rawWeb  string
		hosts   []string
		wantLen int
	}{
		{"unset web url", "", []string{"cdn.example"}, 1},
		{"whitespace-only web url", "   ", []string{"cdn.example"}, 1},
		{"a url with no host", "/just/a/path", []string{"cdn.example"}, 1},
		{"no configured hosts either", "", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withSelfHost(tt.hosts, tt.rawWeb)
			if len(got) != tt.wantLen {
				t.Errorf("withSelfHost(%q, %q) = %q, want %d entries", tt.hosts, tt.rawWeb, got, tt.wantLen)
			}
			for _, host := range got {
				if host == "" {
					t.Errorf("withSelfHost(%q, %q) = %q, contains an empty host", tt.hosts, tt.rawWeb, got)
				}
			}
		})
	}
}

func TestWithSelfHostSkipsADuplicate(t *testing.T) {
	tests := []struct {
		name   string
		hosts  []string
		rawWeb string
	}{
		{"exact duplicate", []string{"bot.example"}, "https://bot.example/"},
		{"existing entry differs in case", []string{"Bot.Example"}, "https://bot.example/"},
		{"self host differs in case", []string{"bot.example"}, "https://BOT.EXAMPLE/"},
		{"duplicate among several", []string{"cdn.example", "bot.example", "img.example"}, "bot.example"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withSelfHost(tt.hosts, tt.rawWeb)

			if len(got) != len(tt.hosts) {
				t.Fatalf("withSelfHost(%q, %q) = %q, want the input unchanged at %d entries",
					tt.hosts, tt.rawWeb, got, len(tt.hosts))
			}
			for i := range tt.hosts {
				if got[i] != tt.hosts[i] {
					t.Errorf("withSelfHost(%q, %q)[%d] = %q, want %q",
						tt.hosts, tt.rawWeb, i, got[i], tt.hosts[i])
				}
			}
		})
	}
}

// An in-place append looks correct until the capacity happens to be spare.
func TestWithSelfHostDoesNotMutateItsInput(t *testing.T) {
	// Spare capacity is the shape that makes an in-place append succeed.
	hosts := make([]string, 0, 8)
	hosts = append(hosts, "cdn.example", "img.example")

	got := withSelfHost(hosts, "https://bot.example/")

	if len(hosts) != 2 {
		t.Errorf("input length = %d after withSelfHost, want 2", len(hosts))
	}
	if hosts[0] != "cdn.example" || hosts[1] != "img.example" {
		t.Errorf("input = %q after withSelfHost, want [cdn.example img.example]", hosts)
	}

	if len(got) != 3 || got[2] != "bot.example" {
		t.Errorf("withSelfHost() = %q, want [cdn.example img.example bot.example]", got)
	}

	got[0] = "overwritten.example"
	if hosts[0] != "cdn.example" {
		t.Errorf("input[0] = %q after writing to the result, want cdn.example; the two slices share backing storage", hosts[0])
	}
}
