// Package reminder holds the platform-neutral, dependency-light logic shared by
// the reminder server, the cron delivery loop and the platform clients:
//
//   - parsing a human-supplied "when" into a concrete UTC instant;
//   - validating and reasoning about a repeat_cron string;
//   - rendering a stored UTC instant in a reminder's timezone.
//
// It deliberately imports no database, no discordgo and no gRPC: everything here
// is a pure function of its inputs so it can be unit-tested in isolation, and so
// both ends of the reverse stream (the server-side cron and the Discord client)
// can import it without pulling in each other's dependencies or creating an
// import cycle.
//
// The delivery payload pushed over the reverse stream used to be a fourth
// responsibility here — the field names of an untyped google.protobuf.Struct,
// defined once so the two ends could not drift apart. It is now the typed
// ginbot.v1.ReminderDelivery message, so the schema is the contract and there is
// nothing left for this package to hold.
package reminder

import (
	"time"
)

// renderLayout is the wall-clock format used by RenderInZone: an unambiguous
// year-first date, 24-hour time and the zone abbreviation.
const renderLayout = "2006-01-02 15:04 MST"

// RenderInZone renders a stored UTC instant in a reminder's IANA timezone,
// falling back to UTC when the zone is empty or unknown.
//
// It is the fallback for platforms with no native timestamp format of their own.
// The Discord client no longer calls it: Discord has <t:UNIX:STYLE>, which each
// viewer's client renders in their own zone, so rendering server-side there
// would print one zone to an audience that does not share it. Matrix and
// anything else without such a tag still need a formatted string, and the
// reminder's stored timezone is what they must format in — which is why this
// stays here rather than moving into pkg/discord. A wire format for one platform
// does not belong in this package either way.
//
// It never errors — an unresolvable zone yields the UTC render rather than a
// failure, so a display path can always produce a string.
func RenderInZone(instant time.Time, timezone string) string {
	loc := time.UTC
	if timezone != "" {
		if resolved, err := time.LoadLocation(timezone); err == nil {
			loc = resolved
		}
	}

	return instant.In(loc).Format(renderLayout)
}
