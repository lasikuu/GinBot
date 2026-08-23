package trigger

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ── Assumed symbols from pkg/trigger (spec §7.5) ─────────────────────────────
//
//	func StripSpoilers(s string) string
//
// Removes complete ||spoiler|| spans left to right (an opening || with no
// closing || after it is left as-is, literal ||), then collapses whitespace
// runs to a single space and trims. Does not exist yet; pkg/trigger does not
// exist as of this writing.

// TestStripSpoilers pins every row of spec §7.5's table verbatim.
func TestStripSpoilers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no spoilers", "hello world", "hello world"},
		{"spoiler in the middle", "a ||secret|| b", "a b"},
		{"whole message is a spoiler", "||secret||", ""},
		{"spoiler with no surrounding spaces", "a||x||b", "a b"},
		{"leftmost complete span only", "||a||b||", "b||"},
		{"unterminated spoiler left as-is", "a ||unterminated", "a ||unterminated"},
		{"whitespace normalisation only", "  spaced   out  ", "spaced out"},
		{"empty spoiler pair", "||||", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripSpoilers(tt.input)
			if got != tt.want {
				t.Errorf("StripSpoilers(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestStripSpoilersMultiByteUTF8: the algorithm scans for the two-byte
// sequence "||", which is ASCII, but the surrounding text may contain
// multi-byte runes. A byte-index-naive implementation that slices the string
// at the wrong offset would split a multi-byte rune and corrupt it (produce
// invalid UTF-8 or garbled characters) rather than cleanly removing the span.
func TestStripSpoilersMultiByteUTF8(t *testing.T) {
	// "héllo" and "wörld" each contain one two-byte UTF-8 rune (é, ö).
	input := "héllo ||secret spoiler|| wörld"
	got := StripSpoilers(input)

	if !utf8.ValidString(got) {
		t.Fatalf("StripSpoilers(%q) = %q, output is not valid UTF-8", input, got)
	}

	want := "héllo wörld"
	if got != want {
		t.Errorf("StripSpoilers(%q) = %q, want %q", input, got, want)
	}
	if strings.Contains(got, "secret spoiler") {
		t.Errorf("StripSpoilers(%q) = %q, spoiler content was not removed", input, got)
	}
}
