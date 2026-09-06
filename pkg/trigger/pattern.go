package trigger

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// BuildPattern returns a case-insensitive regex: exact is anchored, any is
// word-bounded, both quote metacharacters, and regex passes through verbatim.
func BuildPattern(phrase string, mode pb.TriggerMode) string {
	if mode == pb.TriggerMode_TRIGGER_MODE_REGEX {
		return "(?i)" + phrase
	}

	quoted := regexp.QuoteMeta(phrase)
	if mode == pb.TriggerMode_TRIGGER_MODE_EXACT {
		return "(?i)^" + quoted + "$"
	}

	// UNSPECIFIED behaves as ANY throughout this package, per the proto.
	return "(?i)" + wordBoundaryBefore(phrase) + quoted + wordBoundaryAfter(phrase)
}

// wordBoundaryBefore returns the leading boundary group, or "" when the phrase
// starts with a non-word character and the anchor would make it unfireable
// ("++rep", ":)"). RE2's \b is ASCII-only, so the boundary is spelled out as a
// Unicode-aware class instead.
func wordBoundaryBefore(phrase string) string {
	if r, _ := utf8.DecodeRuneInString(phrase); isWordRune(r) {
		return `(?:^|` + nonWordClass + `)`
	}
	return ""
}

// wordBoundaryAfter returns the trailing boundary group, or "" when the phrase
// ends with a non-word character and the anchor would make it unfireable.
func wordBoundaryAfter(phrase string) string {
	if r, _ := utf8.DecodeLastRuneInString(phrase); isWordRune(r) {
		return `(?:$|` + nonWordClass + `)`
	}
	return ""
}

const nonWordClass = `[^\p{L}\p{N}\p{M}_]`

// isWordRune must classify exactly what nonWordClass excludes: a phrase edge the
// two disagree on gets no anchor and silently falls back to substring matching.
// Hence \p{N} rather than unicode.IsDigit, which is Nd only ("cat²" would have
// fired inside "a cat²s"), and \p{M} so a decomposed "hyvä" keeps its anchor.
func isWordRune(r rune) bool {
	return unicode.In(r, unicode.L, unicode.N, unicode.M) || r == '_'
}

// Compile runs at trigger creation or cache load, never per message.
func Compile(phrase string, mode pb.TriggerMode) (*regexp.Regexp, error) {
	if strings.TrimSpace(phrase) == "" {
		return nil, ErrEmptyPhrase
	}
	if len(phrase) > MaxPatternLength {
		return nil, ErrPatternTooLong
	}

	compiled, err := regexp.Compile(BuildPattern(phrase, mode))
	if err != nil {
		return nil, fmt.Errorf("compile trigger pattern: %w", err)
	}

	return compiled, nil
}
