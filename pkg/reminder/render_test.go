package reminder

import (
	"testing"
	"time"
)

// These tests assert the property, not a format string.
var (
	renderInZone = RenderInZone
)

// TestRenderInZoneHelsinkiWallClock: 15:00 UTC is 18:00 Helsinki summer (EEST +3).
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
