package discord

import (
	"strings"
	"testing"
	"time"
)

// knownInstant is 2009-02-13 23:31:30 UTC, whose Unix second is the memorable
// 1234567890. Using it means the expected tag below can be read at a glance
// rather than re-derived from the implementation.
var knownInstant = time.Date(2009, 2, 13, 23, 31, 30, 0, time.UTC)

const knownUnixSeconds = "1234567890"

// TestTimestampTag pins the wire format. Discord parses <t:SECONDS:STYLE>
// literally: a wrong separator or a missing angle bracket does not error, it
// posts the raw text to the channel.
func TestTimestampTag(t *testing.T) {
	tests := []struct {
		name  string
		style timestampStyle
		want  string
	}{
		{name: "long date and time", style: timestampLongDateTime, want: "<t:1234567890:F>"},
		{name: "relative", style: timestampRelative, want: "<t:1234567890:R>"},
		{name: "short time", style: timestampShortTime, want: "<t:1234567890:t>"},
		{name: "long time", style: timestampLongTime, want: "<t:1234567890:T>"},
		{name: "short date", style: timestampShortDate, want: "<t:1234567890:d>"},
		{name: "long date", style: timestampLongDate, want: "<t:1234567890:D>"},
		{name: "short date and time", style: timestampShortDateTime, want: "<t:1234567890:f>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timestampTag(knownInstant, tt.style); got != tt.want {
				t.Errorf("timestampTag(%v) = %q, want %q", tt.style, got, tt.want)
			}
		})
	}
}

// TestTimestampTagIsIndependentOfTheProcessZone is the whole reason for the tag:
// what goes on the wire is an absolute instant, so the bot's own TZ cannot leak
// into what a user sees.
func TestTimestampTagIsIndependentOfTheProcessZone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("load Asia/Tokyo: %v", err)
	}

	utc := timestampTag(knownInstant, timestampLongDateTime)
	tokyo := timestampTag(knownInstant.In(loc), timestampLongDateTime)

	if utc != tokyo {
		t.Errorf("the same instant rendered as %q in UTC and %q in Tokyo", utc, tokyo)
	}
}

// TestTimestampTagTruncatesToSeconds: the tag has no sub-second resolution, so a
// remainder is dropped rather than rounded up into the next second.
func TestTimestampTagTruncatesToSeconds(t *testing.T) {
	withNanos := knownInstant.Add(999 * time.Millisecond)

	if got, want := timestampTag(withNanos, timestampRelative), "<t:"+knownUnixSeconds+":R>"; got != want {
		t.Errorf("timestampTag(sub-second) = %q, want %q", got, want)
	}
}

// TestTimestampWithRelative pins the paired form used for a reminder's fire
// time: the absolute answers "when exactly" and the relative answers "how soon".
func TestTimestampWithRelative(t *testing.T) {
	got := timestampWithRelative(knownInstant)

	if want := "<t:1234567890:F> (<t:1234567890:R>)"; got != want {
		t.Errorf("timestampWithRelative() = %q, want %q", got, want)
	}
	// Both halves must name the same instant, or the two would disagree.
	if strings.Count(got, knownUnixSeconds) != 2 {
		t.Errorf("timestampWithRelative() = %q, want the same instant in both halves", got)
	}
}

// TestTimestampStylesAreDistinct: the styles are single letters and Discord is
// case-sensitive about them, so two colliding constants would silently render
// the wrong thing rather than fail.
func TestTimestampStylesAreDistinct(t *testing.T) {
	styles := map[timestampStyle]string{
		timestampShortTime:     "timestampShortTime",
		timestampLongTime:      "timestampLongTime",
		timestampShortDate:     "timestampShortDate",
		timestampLongDate:      "timestampLongDate",
		timestampShortDateTime: "timestampShortDateTime",
		timestampLongDateTime:  "timestampLongDateTime",
		timestampRelative:      "timestampRelative",
	}

	if len(styles) != 7 {
		t.Errorf("the seven documented styles collapsed into %d distinct values: %v", len(styles), styles)
	}
}
