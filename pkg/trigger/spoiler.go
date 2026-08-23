package trigger

import "strings"

// spoilerMarker is Discord's spoiler-span delimiter.
const spoilerMarker = "||"

// StripSpoilers removes Discord-style ||spoiler|| spans so that hidden text
// cannot fire a trigger, and normalises the surrounding whitespace.
//
// Scanning is left to right and greedy on the opening marker: the leftmost
// pair of markers is always removed as one span, even when a later marker
// would pair differently. An opening marker with no closing marker after it is
// left untouched, literal "||" included, and scanning stops there.
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
			// No closing marker: leave the remainder, including this opening
			// marker, exactly as it is.
			b.WriteString(s[i:])
			break
		}
		end := searchFrom + relEnd + len(spoilerMarker)

		b.WriteString(s[i:start])
		b.WriteByte(' ')
		i = end
	}

	// strings.Fields both collapses whitespace runs and trims the ends.
	return strings.Join(strings.Fields(b.String()), " ")
}
