package discord

import (
	"strings"
	"testing"
	"time"
)

// knownInstant's Unix second is the memorable 1234567890.
var knownInstant = time.Date(2009, 2, 13, 23, 31, 30, 0, time.UTC)

const knownUnixSeconds = "1234567890"

// TestTimestampTag pins the <t:SECONDS:STYLE> wire format; a malformed tag posts
// as raw text rather than erroring.
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

// TestTimestampTagIsIndependentOfTheProcessZone: the wire carries an absolute
// instant, so the bot's TZ cannot leak into what a user sees.
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

// TestTimestampTagTruncatesToSeconds: a sub-second remainder is dropped, not
// rounded up.
func TestTimestampTagTruncatesToSeconds(t *testing.T) {
	withNanos := knownInstant.Add(999 * time.Millisecond)

	if got, want := timestampTag(withNanos, timestampRelative), "<t:"+knownUnixSeconds+":R>"; got != want {
		t.Errorf("timestampTag(sub-second) = %q, want %q", got, want)
	}
}

// TestTimestampWithRelative pins the paired absolute-plus-relative form.
func TestTimestampWithRelative(t *testing.T) {
	got := timestampWithRelative(knownInstant)

	if want := "<t:1234567890:F> (<t:1234567890:R>)"; got != want {
		t.Errorf("timestampWithRelative() = %q, want %q", got, want)
	}
	// Both halves must name the same instant.
	if strings.Count(got, knownUnixSeconds) != 2 {
		t.Errorf("timestampWithRelative() = %q, want the same instant in both halves", got)
	}
}

// TestTimestampStylesAreDistinct: the single-letter styles are case-sensitive, so
// two colliding constants would silently render the wrong thing.
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
