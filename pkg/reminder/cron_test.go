package reminder

import (
	"testing"
	"time"
)

// ── Assumed symbols from pkg/reminder ────────────────────────────────────────
//
//	func ValidateCron(expr string) error
//	func NextOccurrence(expr string, after time.Time, loc *time.Location) (time.Time, error)
//
// Both use the real robfig/cron scheduler underneath, so a semantically-invalid
// but shape-valid cron string ("99 99 99 99 99") is rejected.
var (
	validateCron   = ValidateCron
	nextOccurrence = NextOccurrence
)

// TestValidateCronAccepts pins the forms the scheduler must accept: standard
// 5-field crons, step syntax, named schedules, and @every descriptors.
func TestValidateCronAccepts(t *testing.T) {
	valid := []string{
		"0 9 * * *",
		"*/15 * * * *",
		"@daily",
		"@every 90s",
		"@every 12h",
	}
	for _, expr := range valid {
		t.Run(expr, func(t *testing.T) {
			if err := validateCron(expr); err != nil {
				t.Errorf("ValidateCron(%q) = %v, want nil", expr, err)
			}
		})
	}
}

// TestValidateCronRejects pins the forms that must be refused. The first is the
// important one: "99 99 99 99 99" passes a naive shape regex (five numeric
// fields) but is semantically impossible, so only a real scheduler catches it.
func TestValidateCronRejects(t *testing.T) {
	invalid := []struct {
		name string
		expr string
	}{
		{"out of range fields", "99 99 99 99 99"},
		{"not a cron", "not a cron"},
		{"empty", ""},
		{"three fields", "0 9 *"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateCron(tt.expr); err == nil {
				t.Errorf("ValidateCron(%q) = nil, want error", tt.expr)
			}
		})
	}
}

// TestNextOccurrenceDaily: 0 9 * * * after 08:00 on a plain day returns 09:00
// the same day; after 09:30 it returns 09:00 the next day. UTC loc keeps the
// wall clock and the instant identical, isolating the basic advance.
func TestNextOccurrenceDaily(t *testing.T) {
	loc := time.UTC
	after := time.Date(2026, 6, 15, 8, 0, 0, 0, loc)

	got, err := nextOccurrence("0 9 * * *", after, loc)
	if err != nil {
		t.Fatalf("NextOccurrence error = %v", err)
	}
	want := time.Date(2026, 6, 15, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("NextOccurrence(0 9 * * *, 08:00) = %v, want %v", got, want)
	}

	// Strictly after: from exactly 09:00 the next fire is tomorrow, not now.
	gotNext, err := nextOccurrence("0 9 * * *", want, loc)
	if err != nil {
		t.Fatalf("NextOccurrence (strict) error = %v", err)
	}
	wantNext := time.Date(2026, 6, 16, 9, 0, 0, 0, loc)
	if !gotNext.Equal(wantNext) {
		t.Errorf("NextOccurrence strictly after 09:00 = %v, want %v", gotNext, wantNext)
	}
}

// TestNextOccurrenceDSTSpringForward is the headline correctness test.
//
// Europe/Helsinki springs forward on the last Sunday of March. In 2026 that is
// 29 March: at 03:00 local the clock jumps to 04:00, EET(+2) -> EEST(+3).
//
// A daily "0 9 * * *" reminder must still fire at 09:00 LOCAL wall-clock on the
// 29th, which — because the offset grew by an hour — is a DIFFERENT UTC instant
// than a naive +24h from the 28th would give.
//
// Independently computed:
//
//	after       = 2026-03-28 09:30 Helsinki (EET +2)  = 2026-03-28 07:30 UTC
//	next fire   = 2026-03-29 09:00 Helsinki (EEST +3) = 2026-03-29 06:00 UTC
//
// The UTC gap is 22h30m, not 24h. A scheduler that computes in UTC and ignores
// the zone would return 2026-03-29 07:00 UTC (09:00 EET), which is wrong.
func TestNextOccurrenceDSTSpringForward(t *testing.T) {
	loc := helsinki(t)
	after := time.Date(2026, 3, 28, 9, 30, 0, 0, loc)

	got, err := nextOccurrence("0 9 * * *", after, loc)
	if err != nil {
		t.Fatalf("NextOccurrence (spring) error = %v", err)
	}

	// Constructed in loc, so Go applies the correct EEST offset for that date.
	wantLocal := time.Date(2026, 3, 29, 9, 0, 0, 0, loc)
	wantUTC := time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC)

	if !got.Equal(wantLocal) {
		t.Errorf("NextOccurrence (spring) = %v, want %v (09:00 EEST)", got, wantLocal)
	}
	if !got.UTC().Equal(wantUTC) {
		t.Errorf("NextOccurrence (spring) UTC = %v, want %v", got.UTC(), wantUTC)
	}
	// Guard the arithmetic: the local wall clock is 09:00 and the offset is +3h.
	if h, m, _ := got.In(loc).Clock(); h != 9 || m != 0 {
		t.Errorf("local clock = %02d:%02d, want 09:00", h, m)
	}
	if _, off := got.In(loc).Zone(); off != 3*60*60 {
		t.Errorf("offset = %ds, want +10800 (EEST)", off)
	}
}

// TestNextOccurrenceDSTFallBack is the mirror case. Helsinki falls back on the
// last Sunday of October; in 2026 that is 25 October, EEST(+3) -> EET(+2).
//
// Independently computed:
//
//	after     = 2026-10-24 09:30 Helsinki (EEST +3) = 2026-10-24 06:30 UTC
//	next fire = 2026-10-25 09:00 Helsinki (EET +2)  = 2026-10-25 07:00 UTC
//
// UTC gap 24h30m. A UTC-blind scheduler would return 06:00 UTC (09:00 EEST).
func TestNextOccurrenceDSTFallBack(t *testing.T) {
	loc := helsinki(t)
	after := time.Date(2026, 10, 24, 9, 30, 0, 0, loc)

	got, err := nextOccurrence("0 9 * * *", after, loc)
	if err != nil {
		t.Fatalf("NextOccurrence (fall) error = %v", err)
	}

	wantLocal := time.Date(2026, 10, 25, 9, 0, 0, 0, loc)
	wantUTC := time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC)

	if !got.Equal(wantLocal) {
		t.Errorf("NextOccurrence (fall) = %v, want %v (09:00 EET)", got, wantLocal)
	}
	if !got.UTC().Equal(wantUTC) {
		t.Errorf("NextOccurrence (fall) UTC = %v, want %v", got.UTC(), wantUTC)
	}
	if _, off := got.In(loc).Zone(); off != 2*60*60 {
		t.Errorf("offset = %ds, want +7200 (EET)", off)
	}
}

// TestNextOccurrenceChainsForward: computing the next fire, then feeding it back
// in, advances by one period. Two 09:00 dailies in a row are consecutive days.
func TestNextOccurrenceChainsForward(t *testing.T) {
	loc := time.UTC
	after := time.Date(2026, 6, 15, 8, 0, 0, 0, loc)

	first, err := nextOccurrence("0 9 * * *", after, loc)
	if err != nil {
		t.Fatalf("first NextOccurrence error = %v", err)
	}
	second, err := nextOccurrence("0 9 * * *", first, loc)
	if err != nil {
		t.Fatalf("second NextOccurrence error = %v", err)
	}

	if !second.After(first) {
		t.Errorf("second occurrence %v is not after first %v", second, first)
	}
	if diff := second.Sub(first); diff != 24*time.Hour {
		t.Errorf("gap between occurrences = %v, want 24h", diff)
	}
}
