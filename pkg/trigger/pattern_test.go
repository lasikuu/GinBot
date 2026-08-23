package trigger

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

// TestBuildPattern pins every row of spec §7.4's table.
func TestBuildPattern(t *testing.T) {
	tests := []struct {
		name string
		mode pb.TriggerMode
		want string
	}{
		{"exact", pb.TriggerMode_TRIGGER_MODE_EXACT, `(?i)^` + regexp.QuoteMeta("a.b") + `$`},
		{"any", pb.TriggerMode_TRIGGER_MODE_ANY, `(?i)\b` + regexp.QuoteMeta("a.b") + `\b`},
		{"unspecified same as any", pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED, `(?i)\b` + regexp.QuoteMeta("a.b") + `\b`},
	}

	const phrase = "a.b"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildPattern(phrase, tt.mode)
			if got != tt.want {
				t.Errorf("BuildPattern(%q, %v) = %q, want %q", phrase, tt.mode, got, tt.want)
			}
		})
	}
}

// TestBuildPatternRegexModeIsVerbatim: regex mode returns the phrase unquoted,
// with only a case-insensitivity flag prepended.
func TestBuildPatternRegexModeIsVerbatim(t *testing.T) {
	const phrase = `c\+\+|foo.*`
	want := "(?i)" + phrase
	got := BuildPattern(phrase, pb.TriggerMode_TRIGGER_MODE_REGEX)
	if got != want {
		t.Errorf("BuildPattern(%q, REGEX) = %q, want %q", phrase, got, want)
	}
}

// TestExactModeQuotesMetacharacters: the defining behaviour of exact mode.
// "a.b" must match only the literal text "a.b", never "axb" — if the dot were
// left as regex metacharacter '.', it would match any character there.
func TestExactModeQuotesMetacharacters(t *testing.T) {
	re, err := Compile("a.b", pb.TriggerMode_TRIGGER_MODE_EXACT)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if re.MatchString("axb") {
		t.Error(`exact-mode "a.b" matched "axb"; the dot was not quoted`)
	}
	if !re.MatchString("a.b") {
		t.Error(`exact-mode "a.b" did not match the literal "a.b"`)
	}
}

// TestAnyModeQuotesMetacharacters: an any-mode phrase of "c++" must match the
// literal text "c++", not be interpreted as "one or more c's" (which "++"
// would be an invalid quantifier for anyway, but the point is the phrase is
// quoted, not compiled as regex).
func TestAnyModeQuotesMetacharacters(t *testing.T) {
	re, err := Compile("c++", pb.TriggerMode_TRIGGER_MODE_ANY)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if !re.MatchString("I love c++ a lot") {
		t.Error(`any-mode "c++" did not match the literal text "c++"`)
	}
}

// TestAnyModeDropsUnsatisfiableWordBoundaries: \b only matches where a word
// character abuts a non-word one, so anchoring it against a phrase edge that is
// itself non-word produces a pattern that can never fire in ordinary prose.
// Each side of the boundary is therefore emitted only when it is satisfiable.
func TestAnyModeDropsUnsatisfiableWordBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		phrase string
		want   string
	}{
		{"word on both edges keeps both", "cat", `(?i)\bcat\b`},
		{"non-word trailing edge drops the trailing anchor", "c++", `(?i)\bc\+\+`},
		{"non-word leading edge drops the leading anchor", "++rep", `(?i)\+\+rep\b`},
		{"non-word on both edges drops both", ":)", `(?i):\)`},
		{"non-ascii edges are non-word to Go's \\b", "äö", `(?i)äö`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildPattern(tt.phrase, pb.TriggerMode_TRIGGER_MODE_ANY); got != tt.want {
				t.Errorf("BuildPattern(%q, ANY) = %q, want %q", tt.phrase, got, tt.want)
			}
		})
	}
}

// TestAnyModePunctuationPhrasesFireInProse is the behaviour the asymmetric
// boundary exists to deliver: phrases edged with punctuation are exactly the
// ones a chat trigger is most likely to use.
func TestAnyModePunctuationPhrasesFireInProse(t *testing.T) {
	tests := []struct {
		phrase  string
		message string
		want    bool
	}{
		{"c++", "I love c++ a lot", true},
		{"c++", "c++", true},
		{"c++", "talking about abc++ here", false},
		{":)", "hello :) there", true},
		{".NET", "we ship .NET daily", true},
		{"cat", "category theory", false},
		{"cat", "a cat here", true},
	}

	for _, tt := range tests {
		t.Run(tt.phrase+"/"+tt.message, func(t *testing.T) {
			re, err := Compile(tt.phrase, pb.TriggerMode_TRIGGER_MODE_ANY)
			if err != nil {
				t.Fatalf("Compile(%q): %v", tt.phrase, err)
			}
			if got := re.MatchString(tt.message); got != tt.want {
				t.Errorf("any-mode %q matching %q = %v, want %v", tt.phrase, tt.message, got, tt.want)
			}
		})
	}
}

// TestBuildPatternIsCaseInsensitive: the (?i) flag applies uniformly.
func TestBuildPatternIsCaseInsensitive(t *testing.T) {
	tests := []struct {
		name  string
		mode  pb.TriggerMode
		match string
	}{
		{"exact", pb.TriggerMode_TRIGGER_MODE_EXACT, "HELLO"},
		{"any", pb.TriggerMode_TRIGGER_MODE_ANY, "say HELLO now"},
		{"regex", pb.TriggerMode_TRIGGER_MODE_REGEX, "HELLO"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phrase := "hello"
			if tt.mode == pb.TriggerMode_TRIGGER_MODE_REGEX {
				phrase = "^hello$"
			}
			re, err := Compile(phrase, tt.mode)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if !re.MatchString(tt.match) {
				t.Errorf("pattern for %q did not case-insensitively match %q", phrase, tt.match)
			}
		})
	}
}

// TestAnyModeWordBoundary: any-mode "cat" fires inside "a cat here" (word
// boundaries on both sides) but not inside "category" (no boundary on the
// right).
func TestAnyModeWordBoundary(t *testing.T) {
	re, err := Compile("cat", pb.TriggerMode_TRIGGER_MODE_ANY)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if !re.MatchString("a cat here") {
		t.Error(`any-mode "cat" did not match "a cat here"`)
	}
	if re.MatchString("category") {
		t.Error(`any-mode "cat" matched inside "category"; word boundary not enforced`)
	}
}

// TestCompileRejectsBlankOrWhitespacePhrase.
func TestCompileRejectsBlankOrWhitespacePhrase(t *testing.T) {
	for _, phrase := range []string{"", "   ", "\t\n"} {
		t.Run("", func(t *testing.T) {
			_, err := Compile(phrase, pb.TriggerMode_TRIGGER_MODE_ANY)
			if !errors.Is(err, ErrEmptyPhrase) {
				t.Errorf("Compile(%q) err = %v, want ErrEmptyPhrase", phrase, err)
			}
		})
	}
}

// TestCompileLengthBoundary: exactly MaxPatternLength bytes compiles, one byte
// over is refused with ErrPatternTooLong.
func TestCompileLengthBoundary(t *testing.T) {
	atLimit := strings.Repeat("a", MaxPatternLength)
	if _, err := Compile(atLimit, pb.TriggerMode_TRIGGER_MODE_ANY); err != nil {
		t.Errorf("Compile at exactly MaxPatternLength (%d bytes) failed: %v", MaxPatternLength, err)
	}

	overLimit := strings.Repeat("a", MaxPatternLength+1)
	_, err := Compile(overLimit, pb.TriggerMode_TRIGGER_MODE_ANY)
	if !errors.Is(err, ErrPatternTooLong) {
		t.Errorf("Compile at MaxPatternLength+1 err = %v, want ErrPatternTooLong", err)
	}
}

// TestCompileRejectsUncompilableRegex: "[" is an unterminated character class.
func TestCompileRejectsUncompilableRegex(t *testing.T) {
	_, err := Compile("[", pb.TriggerMode_TRIGGER_MODE_REGEX)
	if err == nil {
		t.Fatal(`Compile("[", REGEX) succeeded, want an error`)
	}
	if errors.Is(err, ErrEmptyPhrase) || errors.Is(err, ErrPatternTooLong) {
		t.Errorf("Compile(\"[\", REGEX) err = %v, want the underlying regexp error, not a length/blank sentinel", err)
	}
}

// TestCompileAcceptsValidRegex.
func TestCompileAcceptsValidRegex(t *testing.T) {
	re, err := Compile(`^(foo|bar)\d+$`, pb.TriggerMode_TRIGGER_MODE_REGEX)
	if err != nil {
		t.Fatalf("Compile valid regex: %v", err)
	}
	if !re.MatchString("foo123") {
		t.Error("compiled regex did not match an intended input")
	}
}
