package trigger

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzStripSpoilers asserts two real invariants that a hand-written table
// cannot exhaustively cover: StripSpoilers must never panic on arbitrary byte
// sequences (a message-processing hot path that panics takes the caller down
// with it), and its output must never contain a complete "||...||" span — if
// one survived, hidden text could still fire a trigger, which is the entire
// point of the function.
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
			// Invalid UTF-8 input is out of scope: Discord messages are valid
			// UTF-8 by construction, and the invariant below (no complete ||
			// span survives) is only meaningful for well-formed text.
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

// hasCompleteSpoilerSpan reports whether s contains an opening "||" followed
// later by a closing "||" — the exact condition StripSpoilers's algorithm is
// specified to eliminate.
func hasCompleteSpoilerSpan(s string) bool {
	open := strings.Index(s, "||")
	if open == -1 {
		return false
	}
	return strings.Contains(s[open+2:], "||")
}
