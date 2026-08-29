package trigger

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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

// TestStripSpoilersMultiByteUTF8 guards against slicing through a multi-byte
// rune while scanning for the ASCII "||".
func TestStripSpoilersMultiByteUTF8(t *testing.T) {
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
