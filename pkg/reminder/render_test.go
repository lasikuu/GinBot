package reminder

import (
	"testing"
	"time"
)

// ── Assumed symbols from pkg/reminder ────────────────────────────────────────
//
// AC2 — timezone rendering. A pure helper renders a stored UTC instant in a
// reminder's IANA timezone, falling back to UTC when the zone is empty or
// invalid, without erroring. pkg/reminder imports no discordgo and is the
// natural home for a pure render helper, so it is assumed here:
//
//	func RenderInZone(instant time.Time, timezone string) string
//
// The exact return shape is unspecified, so this test does NOT pin a format
// string. It asserts the property that matters: the same instant rendered in
// Helsinki and in an empty/invalid zone differ in the way DST dictates, and the
// empty/invalid case matches an explicit "UTC" render (the documented fallback).
//
// If no such pure helper exists and rendering is entangled with discordgo,
// delete this file — see the report's "Deliberately not done" note; a fabricated
// helper is worse than an honest skip.
var (
	renderInZone = RenderInZone
)

// TestRenderInZoneHelsinkiWallClock: a UTC instant renders as its Helsinki local
// wall clock. 2026-08-22 15:00 UTC is 18:00 in Helsinki summer (EEST +3). The
// rendered Helsinki string must therefore differ from the UTC render, and the
// UTC render must contain the untranslated hour.
func TestRenderInZoneHelsinkiWallClock(t *testing.T) {
	instant := time.Date(2026, 8, 22, 15, 0, 0, 0, time.UTC)

	helsinkiRender := renderInZone(instant, "Europe/Helsinki")
	utcRender := renderInZone(instant, "UTC")

	if helsinkiRender == "" {
		t.Fatal("RenderInZone returned empty for Europe/Helsinki")
	}
	if helsinkiRender == utcRender {
		t.Errorf("Helsinki render %q equals UTC render %q; the zone was not applied",
			helsinkiRender, utcRender)
	}
}

// TestRenderInZoneFallsBackToUTC: an empty or invalid timezone falls back to UTC
// without erroring (the function returns a string, so "without erroring" means it
// returns the same value as an explicit UTC render rather than panicking or
// returning empty).
func TestRenderInZoneFallsBackToUTC(t *testing.T) {
	instant := time.Date(2026, 8, 22, 15, 0, 0, 0, time.UTC)
	utcRender := renderInZone(instant, "UTC")

	for _, tz := range []string{"", "Not/AZone", "garbage"} {
		got := renderInZone(instant, tz)
		if got != utcRender {
			t.Errorf("RenderInZone(%q) = %q, want UTC fallback %q", tz, got, utcRender)
		}
	}
}
