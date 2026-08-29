package trigger

import (
	"fmt"
	"regexp"
	"strings"
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

// wordBoundaryBefore returns the leading \b, or "" when the phrase starts with
// a non-word character and the anchor would make it unfireable ("++rep", ":)").
func wordBoundaryBefore(phrase string) string {
	if r, _ := utf8.DecodeRuneInString(phrase); isASCIIWord(r) {
		return `\b`
	}
	return ""
}

func wordBoundaryAfter(phrase string) string {
	if r, _ := utf8.DecodeLastRuneInString(phrase); isASCIIWord(r) {
		return `\b`
	}
	return ""
}

// isASCIIWord mirrors Go's regexp \b, which is ASCII-only: "ä" is not a word
// character here either.
func isASCIIWord(r rune) bool {
	return r == '_' ||
		(r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z')
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
