package discord

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSplitContentNeverExceedsTheLimit is the property splitContent exists for:
// whatever the input, no chunk may be too large for one Discord message.
func TestSplitContentNeverExceedsTheLimit(t *testing.T) {
	tests := []struct {
		name    string
		content string
		limit   int
	}{
		{name: "empty", content: "", limit: 50},
		{name: "fits already", content: "hello", limit: 50},
		{name: "exactly at the limit", content: strings.Repeat("a", 50), limit: 50},
		{name: "many short lines", content: strings.Repeat("line\n", 40), limit: 50},
		{name: "one very long line", content: strings.Repeat("x", 500), limit: 50},
		{name: "mixed short and one long line", content: "short\n" + strings.Repeat("y", 500) + "\nshort again", limit: 50},
		{name: "multi-byte runes", content: strings.Repeat("ä", 200), limit: 50},
		{name: "emoji", content: strings.Repeat("🎲", 200), limit: 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitContent(tt.content, tt.limit)
			for i, chunk := range chunks {
				if len(chunk) > tt.limit {
					t.Errorf("chunk %d has length %d, want at most %d: %q", i, len(chunk), tt.limit, chunk)
				}
				if !utf8.ValidString(chunk) {
					t.Errorf("chunk %d is not valid UTF-8: %q", i, chunk)
				}
			}
		})
	}
}

// TestSplitContentPrefersNewlineBoundaries: splitting must not cut a line in
// half when a newline nearby would keep it whole.
func TestSplitContentPrefersNewlineBoundaries(t *testing.T) {
	content := "aaaa\nbbbb\ncccc\ndddd"
	chunks := splitContent(content, 10)

	for i, chunk := range chunks {
		trimmed := strings.TrimSuffix(chunk, "\n")
		if trimmed == "" {
			continue
		}
		lines := strings.Split(trimmed, "\n")
		for _, line := range lines {
			if line != "aaaa" && line != "bbbb" && line != "cccc" && line != "dddd" {
				t.Errorf("chunk %d contains a line split mid-way: %q (chunk %q)", i, line, chunk)
			}
		}
	}
}

// TestSplitContentCutsAnOverLongLineOnARuneBoundary: a single line longer than
// the limit has no newline to split on, so the cut must still land on a rune
// boundary. Finnish "ä"/"ö" and an emoji are all multi-byte in UTF-8.
func TestSplitContentCutsAnOverLongLineOnARuneBoundary(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		limit int
	}{
		{name: "Finnish letters", line: strings.Repeat("ää öö ", 30), limit: 20},
		{name: "emoji", line: strings.Repeat("🎲🎲🎲🎲", 30), limit: 20},
		{name: "mixed ASCII and multi-byte", line: strings.Repeat("a", 15) + strings.Repeat("ö", 20), limit: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitContent(tt.line, tt.limit)

			for i, chunk := range chunks {
				if len(chunk) > tt.limit {
					t.Errorf("chunk %d has length %d, want at most %d", i, len(chunk), tt.limit)
				}
				if !utf8.ValidString(chunk) {
					t.Errorf("chunk %d is not valid UTF-8: %q", i, chunk)
				}
			}
		})
	}
}

// TestSplitContentFitsInOneChunk: input already within the limit is not split.
func TestSplitContentFitsInOneChunk(t *testing.T) {
	content := "short content"
	chunks := splitContent(content, 2000)

	if len(chunks) != 1 {
		t.Fatalf("splitContent produced %d chunks for content already within the limit, want 1", len(chunks))
	}
	if chunks[0] != content {
		t.Errorf("chunk = %q, want the content unchanged: %q", chunks[0], content)
	}
}

// TestSplitContentEmptyInputReturnsNothing: no chunks for no content, so a
// caller does not send an empty message.
func TestSplitContentEmptyInputReturnsNothing(t *testing.T) {
	if chunks := splitContent("", 100); len(chunks) != 0 {
		t.Errorf("splitContent(\"\") = %d chunks, want 0", len(chunks))
	}
}

// nonSeparatorRuneCount counts runes that are not consumed purely to join
// chunks back together, so the concatenation check below tolerates a
// separator (such as a stripped trailing newline) without being fooled by it.
func nonSeparatorRuneCount(s string) int {
	return utf8.RuneCountInString(strings.Map(func(r rune) rune {
		if r == '\n' {
			return -1
		}
		return r
	}, s))
}

// TestSplitContentPreservesEveryNonSeparatorCharacter: splitting must not drop
// or duplicate content, only decide where the separators fall.
func TestSplitContentPreservesEveryNonSeparatorCharacter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		limit   int
	}{
		{name: "many lines", content: strings.Repeat("gm\n", 100), limit: 20},
		{name: "one long line", content: strings.Repeat("ä", 300), limit: 37},
		{name: "mixed", content: "intro\n" + strings.Repeat("body text ", 50) + "\noutro", limit: 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitContent(tt.content, tt.limit)

			want := nonSeparatorRuneCount(tt.content)
			got := 0
			for _, chunk := range chunks {
				got += nonSeparatorRuneCount(chunk)
			}
			if got != want {
				t.Errorf("reassembled non-separator rune count = %d, want %d", got, want)
			}
		})
	}
}
