package reminder

import (
	"testing"
	"time"
)

var (
	validateCron   = ValidateCron
	nextOccurrence = NextOccurrence
)

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

	// Strictly after: from exactly 09:00 the next fire is tomorrow.
	gotNext, err := nextOccurrence("0 9 * * *", want, loc)
	if err != nil {
		t.Fatalf("NextOccurrence (strict) error = %v", err)
	}
	wantNext := time.Date(2026, 6, 16, 9, 0, 0, 0, loc)
	if !gotNext.Equal(wantNext) {
		t.Errorf("NextOccurrence strictly after 09:00 = %v, want %v", gotNext, wantNext)
	}
}

// TestNextOccurrenceDSTSpringForward: Helsinki goes EET(+2) -> EEST(+3) on
// 2026-03-29, so a daily 09:00 local advances 22h30m in UTC, not 24h.
func TestNextOccurrenceDSTSpringForward(t *testing.T) {
	loc := helsinki(t)
	after := time.Date(2026, 3, 28, 9, 30, 0, 0, loc)

	got, err := nextOccurrence("0 9 * * *", after, loc)
	if err != nil {
		t.Fatalf("NextOccurrence (spring) error = %v", err)
	}

	wantLocal := time.Date(2026, 3, 29, 9, 0, 0, 0, loc)
	wantUTC := time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC)

	if !got.Equal(wantLocal) {
		t.Errorf("NextOccurrence (spring) = %v, want %v (09:00 EEST)", got, wantLocal)
	}
	if !got.UTC().Equal(wantUTC) {
		t.Errorf("NextOccurrence (spring) UTC = %v, want %v", got.UTC(), wantUTC)
	}
	if h, m, _ := got.In(loc).Clock(); h != 9 || m != 0 {
		t.Errorf("local clock = %02d:%02d, want 09:00", h, m)
	}
	if _, off := got.In(loc).Zone(); off != 3*60*60 {
		t.Errorf("offset = %ds, want +10800 (EEST)", off)
	}
}

// TestNextOccurrenceDSTFallBack: the mirror case, EEST(+3) -> EET(+2) on
// 2026-10-25, so the UTC gap is 24h30m.
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
