package config

import (
	"net/url"
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

// hostOf extracts the hostname from a URL, tolerating a bare host with no
// scheme.
//
// GINBOT_WEB_URL is documented as the bot's web ADDRESS, so operators write
// anything from "bot.example" to "https://bot.example/path". Prepending "//"
// when there is no scheme is what makes the bare forms work: url.Parse reads
// "bot.example:443" as scheme "bot.example" with opaque "443" otherwise, and
// would report no host at all.
//
// Unparseable input yields "", which withSelfHost then skips — a malformed web
// URL should cost the self-exclusion, not startup.
//
// A leading "www." is deliberately left on: urlnorm.New strips it already, and
// stripping it twice is just an opportunity for the two to disagree.
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

// withSelfHost returns hosts plus the bot's own host, deduplicated.
//
// This is what makes "the bot's own links are never indexed" true by default
// rather than only when an operator remembers to repeat the host in
// GINBOT_REPOST_EXCLUDED_HOSTS. The result is a new slice: the input comes
// straight from repostExcludedHosts and appending in place would be an
// aliasing surprise for no gain.
func withSelfHost(hosts []string, rawWebURL string) []string {
	out := make([]string, 0, len(hosts)+1)
	out = append(out, hosts...)

	self := hostOf(rawWebURL)
	if self == "" {
		return out
	}

	// Compared with any www. prefix removed on BOTH sides, because that is the
	// form urlnorm.New reduces every entry to before matching. Without it,
	// GINBOT_WEB_URL=https://www.bot.example alongside a configured
	// bot.example appends a second entry that collapses onto the first anyway —
	// harmless, but it makes the list lie about what is configured.
	for _, host := range out {
		if strings.EqualFold(bareHost(host), bareHost(self)) {
			return out
		}
	}

	return append(out, self)
}

// bareHost strips the leading www. that urlnorm.New strips too, so two
// spellings of one host compare equal.
func bareHost(host string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
}
