package config

import (
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// RepostOptions thresholds are configuration so they tune without a redeploy.
type RepostOptions struct {
	// Enabled is opt-in: the feature needs the privileged MESSAGE_CONTENT intent.
	Enabled bool
	// Inclusive Hamming bounds; TierProbable is clamped to repost.MaxDistance.
	TierIdentical int
	TierHigh      int
	TierProbable  int
	// Minimum decoded dimensions for perceptual matching eligibility.
	MinWidth  int
	MinHeight int
	// MinEntropy is the Shannon entropy floor in bits (0..8).
	MinEntropy float64
	// ExcludedHosts are hosts URL canonicalisation must never index.
	ExcludedHosts []string
	// FFmpegPath is empty to look "ffmpeg" up on PATH. See ADR-0006.
	FFmpegPath string
}

func repostEnabled() bool {
	return os.Getenv("GINBOT_REPOST") == "true"
}

// Falls back to def rather than failing startup over a tuning knob.
func repostIntThreshold(envVar string, def int) int {
	value := os.Getenv(envVar)
	if value == "" {
		return def
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Z.Warn("failed to parse repost threshold; using the default.",
			zap.String("var", envVar), zap.Error(err))
		return def
	}

	return parsed
}

func repostTierIdentical() int {
	return repostIntThreshold("GINBOT_REPOST_TIER_IDENTICAL", 0)
}

func repostTierHigh() int {
	return repostIntThreshold("GINBOT_REPOST_TIER_HIGH", 3)
}

func repostTierProbable() int {
	return repostIntThreshold("GINBOT_REPOST_TIER_PROBABLE", 7)
}

func repostMinWidth() int {
	return repostIntThreshold("GINBOT_REPOST_MIN_WIDTH", 128)
}

func repostMinHeight() int {
	return repostIntThreshold("GINBOT_REPOST_MIN_HEIGHT", 128)
}

func repostMinEntropy() float64 {
	value := os.Getenv("GINBOT_REPOST_MIN_ENTROPY")
	if value == "" {
		return 3.0
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Z.Warn("failed to parse GINBOT_REPOST_MIN_ENTROPY; using the default.", zap.Error(err))
		return 3.0
	}

	return parsed
}

func repostExcludedHosts() []string {
	configured := strings.Split(os.Getenv("GINBOT_REPOST_EXCLUDED_HOSTS"), ",")

	hosts := make([]string, 0, len(configured))
	for _, host := range configured {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		hosts = append(hosts, host)
	}

	return hosts
}

func repostFFmpegPath() string {
	return os.Getenv("GINBOT_REPOST_FFMPEG_PATH")
}

// url.Parse reads "bot.example:443" as a scheme unless "//" is prepended.
// Unparseable input yields "", and a leading "www." is kept.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return strings.ToLower(parsed.Hostname())
}

// withSelfHost returns a new, deduplicated slice; it never appends in place.
func withSelfHost(hosts []string, rawWebURL string) []string {
	out := make([]string, 0, len(hosts)+1)
	out = append(out, hosts...)

	self := hostOf(rawWebURL)
	if self == "" {
		return out
	}

	// Compared bare on both sides, the form urlnorm.New reduces entries to.
	for _, host := range out {
		if strings.EqualFold(bareHost(host), bareHost(self)) {
			return out
		}
	}

	return append(out, self)
}

// bareHost strips the leading www. that urlnorm.New strips too.
func bareHost(host string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
}
