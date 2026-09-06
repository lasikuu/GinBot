package trigger

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

func TestBuildPattern(t *testing.T) {
	const wordBoundary = `(?:^|[^\p{L}\p{N}\p{M}_])`
	const wordBoundaryEnd = `(?:$|[^\p{L}\p{N}\p{M}_])`

	tests := []struct {
		name string
		mode pb.TriggerMode
		want string
	}{
		{"exact", pb.TriggerMode_TRIGGER_MODE_EXACT, `(?i)^` + regexp.QuoteMeta("a.b") + `$`},
		{"any", pb.TriggerMode_TRIGGER_MODE_ANY, "(?i)" + wordBoundary + regexp.QuoteMeta("a.b") + wordBoundaryEnd},
		{"unspecified same as any", pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED, "(?i)" + wordBoundary + regexp.QuoteMeta("a.b") + wordBoundaryEnd},
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

func TestBuildPatternRegexModeIsVerbatim(t *testing.T) {
	const phrase = `c\+\+|foo.*`
	want := "(?i)" + phrase
	got := BuildPattern(phrase, pb.TriggerMode_TRIGGER_MODE_REGEX)
	if got != want {
		t.Errorf("BuildPattern(%q, REGEX) = %q, want %q", phrase, got, want)
	}
}

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

func TestAnyModeQuotesMetacharacters(t *testing.T) {
	re, err := Compile("c++", pb.TriggerMode_TRIGGER_MODE_ANY)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if !re.MatchString("I love c++ a lot") {
		t.Error(`any-mode "c++" did not match the literal text "c++"`)
	}
}

func TestAnyModeDropsUnsatisfiableWordBoundaries(t *testing.T) {
	const wordBoundary = `(?:^|[^\p{L}\p{N}\p{M}_])`
	const wordBoundaryEnd = `(?:$|[^\p{L}\p{N}\p{M}_])`

	tests := []struct {
		name   string
		phrase string
		want   string
	}{
		{"word on both edges keeps both", "cat", "(?i)" + wordBoundary + "cat" + wordBoundaryEnd},
		{"non-word trailing edge drops the trailing anchor", "c++", "(?i)" + wordBoundary + `c\+\+`},
		{"non-word leading edge drops the leading anchor", "++rep", `(?i)\+\+rep` + wordBoundaryEnd},
		{"non-word on both edges drops both", ":)", `(?i):\)`},
		{
			name:   "non-ascii word runes keep both boundaries, unlike ASCII-only \\b",
			phrase: "äö",
			want:   "(?i)" + wordBoundary + "äö" + wordBoundaryEnd,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildPattern(tt.phrase, pb.TriggerMode_TRIGGER_MODE_ANY); got != tt.want {
				t.Errorf("BuildPattern(%q, ANY) = %q, want %q", tt.phrase, got, tt.want)
			}
		})
	}
}

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

// TestAnyModeUnicodeWordBoundaryMatrix is the specified matrix for the
// Unicode-aware boundary: RE2's \b was ASCII-only, so "hyvä" used to fire
// inside "hyväksyä" and "äcatä" used to fire on "cat".
func TestAnyModeUnicodeWordBoundaryMatrix(t *testing.T) {
	tests := []struct {
		phrase  string
		message string
		want    bool
	}{
		{"cat", "a cat here", true},
		{"cat", "cat", true},
		{"cat", "cat cat", true},
		{"cat", "wow, cat.", true},
		{"cat", "category theory", false},
		{"cat", "äcatä", false},
		{"hyvä", "hyvä juttu", true},
		{"hyvä", "no hyvä", true},
		{"hyvä", "hyväksyä", false},
		{"äö", "äö", true},
		{"äö", "käöp", false},
		{"c++", "c++ rocks", true},
		{"c++", "abc++ here", false},
		{"++rep", "++rep", true},
		{":)", "well :)", true},
		// \p{N} is Nd+Nl+No; a helper using unicode.IsDigit sees only Nd, drops
		// the anchor entirely, and silently falls back to substring matching.
		{"cat²", "a cat² here", true},
		{"cat²", "a cat²s", false},
		{"½cup", "add ½cup now", true},
		{"½cup", "x½cupy", false},
		{"Ⅷ", "chapter Ⅷ", true},
		{"Ⅷ", "aⅧb", false},
		// Decomposed: the phrase ends in a combining mark, which is part of the
		// word rather than a delimiter.
		{"hyva\u0308", "hyva\u0308 juttu", true},
		{"hyva\u0308", "hyva\u0308ksya\u0308", false},
		{"cat", "cat\u0301egory", false},
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

// TestAnyModeBoundaryGroupsConsumeADelimiterButStillMatch: the boundary
// groups consume the delimiter character rather than being zero-width, so a
// phrase right at either edge of the string, and two occurrences separated
// by only one delimiter, must still be found by MatchString (the only
// production consumer; a non-overlapping FindAll-style scan would behave
// differently, but nothing in this codebase does that).
func TestAnyModeBoundaryGroupsConsumeADelimiterButStillMatch(t *testing.T) {
	re, err := Compile("cat", pb.TriggerMode_TRIGGER_MODE_ANY)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{"occurrence at the very start of the string", "cat is nice", true},
		{"occurrence at the very end of the string", "I love cat", true},
		{"the whole string is exactly the phrase", "cat", true},
		{"two occurrences separated by one delimiter", "cat cat", true},
		{"adjacent with no delimiter is one unbroken word, not two hits", "catcat", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := re.MatchString(tt.message); got != tt.want {
				t.Errorf("MatchString(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

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

func TestCompileRejectsUncompilableRegex(t *testing.T) {
	_, err := Compile("[", pb.TriggerMode_TRIGGER_MODE_REGEX)
	if err == nil {
		t.Fatal(`Compile("[", REGEX) succeeded, want an error`)
	}
	if errors.Is(err, ErrEmptyPhrase) || errors.Is(err, ErrPatternTooLong) {
		t.Errorf("Compile(\"[\", REGEX) err = %v, want the underlying regexp error, not a length/blank sentinel", err)
	}
}

func TestCompileAcceptsValidRegex(t *testing.T) {
	re, err := Compile(`^(foo|bar)\d+$`, pb.TriggerMode_TRIGGER_MODE_REGEX)
	if err != nil {
		t.Fatalf("Compile valid regex: %v", err)
	}
	if !re.MatchString("foo123") {
		t.Error("compiled regex did not match an intended input")
	}
}
