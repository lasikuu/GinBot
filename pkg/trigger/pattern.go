package trigger

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// BuildPattern returns the regular expression source matching phrase in mode.
//
// Exact and any modes quote the phrase, so a phrase containing regex
// metacharacters matches those characters literally: an exact-mode phrase of
// "a.b" must not match "axb". Regex mode returns the phrase verbatim, which is
// why it is clearance-gated.
func BuildPattern(phrase string, mode pb.TriggerMode) string {
	if mode == pb.TriggerMode_TRIGGER_MODE_REGEX {
		return "(?i)" + phrase
	}

	quoted := regexp.QuoteMeta(phrase)
	if mode == pb.TriggerMode_TRIGGER_MODE_EXACT {
		return "(?i)^" + quoted + "$"
	}

	// TRIGGER_MODE_ANY and TRIGGER_MODE_UNSPECIFIED share this pattern:
	// UNSPECIFIED behaves as ANY everywhere in this package, per the proto's
	// own doc comment. No .*-wrapper: regexp.MatchString is already a
	// substring search, so wrapping the boundary pattern in .* on both sides
	// would match identically at strictly more cost.
	return "(?i)" + wordBoundaryBefore(phrase) + quoted + wordBoundaryAfter(phrase)
}

// wordBoundaryBefore returns the leading \b for an any-mode phrase, or "" when
// that anchor could never match.
//
// \b only matches where a word character abuts a non-word one. A phrase that
// starts with a non-word character therefore has no satisfiable boundary in
// front of it once anything else non-word precedes it, so anchoring there makes
// phrases like "++rep" or ":)" effectively unfireable. Dropping the anchor on
// that side is deliberately permissive: a false match is recoverable, a trigger
// that can never fire is not.
func wordBoundaryBefore(phrase string) string {
	if r, _ := utf8.DecodeRuneInString(phrase); isASCIIWord(r) {
		return `\b`
	}
	return ""
}

// wordBoundaryAfter returns the trailing \b for an any-mode phrase, or "" when
// that anchor could never match. See wordBoundaryBefore.
func wordBoundaryAfter(phrase string) string {
	if r, _ := utf8.DecodeLastRuneInString(phrase); isASCIIWord(r) {
		return `\b`
	}
	return ""
}

// isASCIIWord reports whether r is a word character as Go's regexp \b defines
// it. Go's \b is ASCII-only, so a phrase edged with a non-ASCII letter such as
// "ä" is treated as non-word here too, matching what the engine will actually
// do rather than what Unicode would suggest.
func isASCIIWord(r rune) bool {
	return r == '_' ||
		(r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z')
}

// Compile builds the matcher for a phrase in a mode.
//
// Compiling happens once, when a trigger is created or the cached set is
// loaded, never per message.
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
