package trigger

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzStripSpoilers(f *testing.F) {
	seeds := []string{
		"",
		"||",
		"||||",
		"a ||b|| c",
		"||a||b||",
		"a ||unterminated",
		"héllo ||wörld||",
		"|| ||",
		strings.Repeat("|", 50),
		"\x00||\x01||\x02",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got := StripSpoilers(s)

		if !utf8.ValidString(s) {
			// Discord messages are valid UTF-8 by construction.
			return
		}

		if !utf8.ValidString(got) {
			t.Fatalf("StripSpoilers(%q) = %q, output is not valid UTF-8", s, got)
		}

		if hasCompleteSpoilerSpan(got) {
			t.Fatalf("StripSpoilers(%q) = %q, a complete ||..|| span survived", s, got)
		}
	})
}

func hasCompleteSpoilerSpan(s string) bool {
	_, after, ok := strings.Cut(s, "||")
	if !ok {
		return false
	}
	return strings.Contains(after, "||")
}
