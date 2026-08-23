package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// RepostOptions configures WANHA repost detection.
//
// Every threshold here is configuration DELIBERATELY, not a constant: they
// are starting points drawn from the literature (docs/plans/wanha.md "Open
// items"), not values validated against any particular community's content,
// and the user declined a log-only shadow period — so tuning happens live,
// by editing environment variables, not by a code change and a redeploy.
type RepostOptions struct {
	// Enabled gates the whole feature off by default: it needs the
	// privileged MESSAGE_CONTENT intent on Discord, exactly like
	// DISCORD_MESSAGE_CONTENT, so it must be opt-in.
	Enabled bool
	// TierIdentical, TierHigh and TierProbable are inclusive Hamming-distance
	// upper bounds for repost.Tiers. TierProbable is clamped to
	// repost.MaxDistance by repost.Tiers.Normalise wherever these are
	// consumed — going higher would silently break the pigeonhole recall
	// guarantee the whole matching scheme rests on.
	TierIdentical int
	TierHigh      int
	TierProbable  int
	// MinWidth and MinHeight are the minimum decoded dimensions an image
	// must have to be eligible for perceptual matching (W4).
	MinWidth  int
	MinHeight int
	// MinEntropy is the Shannon entropy floor, in bits (0..8), below which an
	// image is excluded from perceptual matching as near-blank or
	// solid-colour (W4).
	MinEntropy float64
	// ExcludedHosts are additional hosts URL canonicalisation must never
	// index — the bot's own web URL, typically.
	ExcludedHosts []string
	// FFmpegPath is the ffmpeg binary to shell out to for video first-frame
	// extraction (ADR-0006). Empty means look "ffmpeg" up on PATH.
	FFmpegPath string
}

func repostEnabled() bool {
	return os.Getenv("GINBOT_REPOST") == "true"
}

// repostIntThreshold parses an integer threshold, warning and falling back to
// def on a bad value rather than failing startup over a tuning knob.
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

// repostExcludedHosts parses a comma-separated host list, mirroring
// commandPrefixes: an empty or all-blank value yields no exclusions, rather
// than one empty-string entry that would (harmlessly, but confusingly) never
// match anything.
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
