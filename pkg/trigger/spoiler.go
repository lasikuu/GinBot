package trigger

import "strings"

const spoilerMarker = "||"

// StripSpoilers removes Discord ||spoiler|| spans so hidden text cannot fire a
// trigger, and collapses whitespace. Markers pair leftmost-first; an unclosed
// opening marker and everything after it is left verbatim.
func StripSpoilers(s string) string {
	var b strings.Builder

	i := 0
	for i < len(s) {
		relStart := strings.Index(s[i:], spoilerMarker)
		if relStart == -1 {
			b.WriteString(s[i:])
			break
		}
		start := i + relStart

		searchFrom := start + len(spoilerMarker)
		relEnd := strings.Index(s[searchFrom:], spoilerMarker)
		if relEnd == -1 {
			b.WriteString(s[i:])
			break
		}
		end := searchFrom + relEnd + len(spoilerMarker)

		b.WriteString(s[i:start])
		b.WriteByte(' ')
		i = end
	}

	return strings.Join(strings.Fields(b.String()), " ")
}
