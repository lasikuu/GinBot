package reminder

import (
	"testing"
	"time"
)

// ── Assumed symbols from pkg/reminder ────────────────────────────────────────
//
// Every symbol this file depends on is aliased here so a rename during
// reconciliation is a one-line fix rather than a scatter of edits.
//
//	func ParseDuration(input string) (time.Duration, error)
var (
	parseDuration = ParseDuration
)

// TestParseDurationComposite asserts the headline composite form and its exact
// resulting time.Duration, computed independently:
//
//	4M = 4*30d = 120d, +2d = 122d, +8h, +30s
//	= 122*24h + 8h + 30s = 2936h0m30s = 10569630000000000 ns.
//
// M is month (30 days) and is case-sensitive against m (minute).
func TestParseDurationComposite(t *testing.T) {
	got, err := parseDuration("4M2d8h30s")
	if err != nil {
		t.Fatalf("ParseDuration(4M2d8h30s) error = %v, want nil", err)
	}

	// Computed by hand, not by re-deriving with the same code under test.
	want := 122*24*time.Hour + 8*time.Hour + 30*time.Second
	if got != want {
		t.Errorf("ParseDuration(4M2d8h30s) = %v (%d ns), want %v (%d ns)",
			got, int64(got), want, int64(want))
	}
	if int64(want) != 10569630000000000 {
		t.Fatalf("test arithmetic drifted: want ns = %d, expected 10569630000000000", int64(want))
	}
}

// TestParseDurationSingleUnits covers each unit in isolation. This pins the unit
// table: M=month(30d), w=week, d=day, h=hour, m=minute, s=second.
func TestParseDurationSingleUnits(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Duration
	}{
		{"seconds", "30s", 30 * time.Second},
		{"minutes", "5m", 5 * time.Minute},
		{"hours", "2h", 2 * time.Hour},
		{"days", "3d", 3 * 24 * time.Hour},
		{"weeks", "1w", 7 * 24 * time.Hour},
		{"month", "1M", 30 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if err != nil {
				t.Fatalf("ParseDuration(%q) error = %v, want nil", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestParseDurationCaseSensitiveMonthVsMinute is the load-bearing case: M and m
// must not be conflated. 1M is a month (30 days), 1m is a minute.
func TestParseDurationCaseSensitiveMonthVsMinute(t *testing.T) {
	month, err := parseDuration("1M")
	if err != nil {
		t.Fatalf("ParseDuration(1M) error = %v", err)
	}
	minute, err := parseDuration("1m")
	if err != nil {
		t.Fatalf("ParseDuration(1m) error = %v", err)
	}
	if month == minute {
		t.Fatalf("1M and 1m parsed to the same duration %v; case sensitivity lost", month)
	}
	if month != 30*24*time.Hour {
		t.Errorf("1M = %v, want 720h (30 days)", month)
	}
	if minute != time.Minute {
		t.Errorf("1m = %v, want 1m", minute)
	}
}

// TestParseDurationRejects covers everything the parser must refuse rather than
// silently coerce. A parser that guesses here corrupts every downstream fire
// time, so each of these must return a non-nil error.
func TestParseDurationRejects(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"zero total", "0s"},
		{"negative", "-5m"},
		{"missing number", "h"},
		{"unknown unit", "5y"},
		{"bare number no unit", "5"},
		{"garbage", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if err == nil {
				t.Errorf("ParseDuration(%q) = %v, want error", tt.input, got)
			}
		})
	}
}

// TestParseDurationRepeatedUnit documents the chosen expectation for a repeated
// unit. INTERPRETATION: a repeated unit (5m5m) is rejected — the composite form
// is a strict descending unit sequence, and a repeat is almost always a typo.
// If reconciliation reveals the implementation sums repeats instead, flip the
// assertion below (want 10m, err == nil).
func TestParseDurationRepeatedUnit(t *testing.T) {
	got, err := parseDuration("5m5m")
	if err == nil {
		t.Errorf("ParseDuration(5m5m) = %v, want error (repeated unit rejected)", got)
	}
}

// TestParseDurationOverflow asserts a value large enough to overflow int64
// nanoseconds errors rather than wrapping to a negative or truncated duration.
// time.Duration is int64 ns; max is ~292 years. 4000000000 hours is far past it.
func TestParseDurationOverflow(t *testing.T) {
	got, err := parseDuration("4000000000h")
	if err == nil {
		t.Errorf("ParseDuration(4000000000h) = %v, want overflow error", got)
	}
	if got < 0 {
		t.Errorf("ParseDuration overflowed to a negative duration %v; it wrapped instead of erroring", got)
	}
}
