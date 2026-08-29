// Package reminder parses a human-supplied "when" into a UTC instant, validates
// repeat_cron strings, and renders a stored instant in a reminder's timezone.
// It must stay free of database, discordgo and RPC imports so both ends of the
// reverse stream can import it.
package reminder

import (
	"time"
)

const renderLayout = "2006-01-02 15:04 MST"

// RenderInZone renders a stored UTC instant in a reminder's IANA timezone. It
// never errors: an empty or unresolvable zone falls back to UTC.
func RenderInZone(instant time.Time, timezone string) string {
	loc := time.UTC
	if timezone != "" {
		if resolved, err := time.LoadLocation(timezone); err == nil {
			loc = resolved
		}
	}

	return instant.In(loc).Format(renderLayout)
}
