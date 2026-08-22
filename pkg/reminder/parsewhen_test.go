package reminder

import (
	"testing"
	"time"
)

// ── Assumed symbols from pkg/reminder ────────────────────────────────────────
//
//	func ParseWhen(input string, now time.Time, loc *time.Location) (time.Time, error)
//
// now is injected so every test is deterministic; no test calls time.Now().
var (
	parseWhen = ParseWhen
)

// helsinki loads Europe/Helsinki once. A missing zoneinfo database is an
// environment failure, not a test failure, so it fatals.
func helsinki(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Helsinki")
	if err != nil {
		t.Fatalf("load Europe/Helsinki: %v", err)
	}
	return loc
}

// TestParseWhenDurationIsNowPlusDuration: a duration input resolves to now plus
// that duration, in UTC. now is fixed.
func TestParseWhenDurationIsNowPlusDuration(t *testing.T) {
	loc := helsinki(t)
	// A summer instant so the zone is EEST; the result must still be UTC.
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	got, err := parseWhen("2h30m", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(2h30m) error = %v", err)
	}
	want := now.Add(2*time.Hour + 30*time.Minute).UTC()
	if !got.UTC().Equal(want) {
		t.Errorf("ParseWhen(2h30m) = %v, want %v", got.UTC(), want)
	}
}

// TestParseWhenZonelessAbsoluteInterpretedInLoc: an absolute zoneless datetime
// is read in loc, then converted to UTC. 2026-08-22 18:00 in Helsinki summer
// (EEST, UTC+3) is 15:00 UTC. Computed independently below.
func TestParseWhenZonelessAbsoluteInterpretedInLoc(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	got, err := parseWhen("2026-08-22 18:00", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(2026-08-22 18:00) error = %v", err)
	}

	// 18:00 local in Helsinki summer == 15:00 UTC. Built directly in loc.
	want := time.Date(2026, 8, 22, 18, 0, 0, 0, loc).UTC()
	if !got.UTC().Equal(want) {
		t.Errorf("ParseWhen(2026-08-22 18:00) = %v, want %v (15:00 UTC)", got.UTC(), want)
	}
	if want.Hour() != 15 {
		t.Fatalf("test arithmetic drifted: want UTC hour = %d, expected 15", want.Hour())
	}
}

// TestParseWhenAbsoluteWithOffsetRespected: an explicit offset in the input is
// authoritative; loc must not override it. 2026-08-22T18:00:00+00:00 is 18:00
// UTC regardless of loc being Helsinki.
func TestParseWhenAbsoluteWithOffsetRespected(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	got, err := parseWhen("2026-08-22T18:00:00+00:00", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen with explicit offset error = %v", err)
	}
	want := time.Date(2026, 8, 22, 18, 0, 0, 0, time.UTC)
	if !got.UTC().Equal(want) {
		t.Errorf("ParseWhen(...+00:00) = %v, want %v", got.UTC(), want)
	}
}

// TestParseWhenISOWithAndWithoutSeconds: both 2026-08-22T18:00 and
// 2026-08-22T18:00:30 parse. The seconds field, when present, is honoured.
func TestParseWhenISOWithAndWithoutSeconds(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	withoutSecs, err := parseWhen("2026-08-22T18:00", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(2026-08-22T18:00) error = %v", err)
	}
	withSecs, err := parseWhen("2026-08-22T18:00:30", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(2026-08-22T18:00:30) error = %v", err)
	}

	// The seconds variant is exactly 30s past the minute variant.
	if diff := withSecs.Sub(withoutSecs); diff != 30*time.Second {
		t.Errorf("seconds not honoured: withSecs - withoutSecs = %v, want 30s", diff)
	}
}

// TestParseWhenDayFirstAmbiguity is the ambiguity contract: 01/02/2026 is
// 1 February 2026, NOT 2 January. Day-first, not US month-first.
func TestParseWhenDayFirstAmbiguity(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	got, err := parseWhen("01/02/2026", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(01/02/2026) error = %v", err)
	}
	local := got.In(loc)
	if local.Month() != time.February {
		t.Errorf("ParseWhen(01/02/2026) month = %v, want February (day-first)", local.Month())
	}
	if local.Day() != 1 {
		t.Errorf("ParseWhen(01/02/2026) day = %d, want 1", local.Day())
	}
}

// TestParseWhenYearlessSlashDateUsesInjectedNow is the purity contract: a
// two-field slash date has no year, and the year it defaults to must come from
// the INJECTED now, not from the wall clock.
//
// It used to read time.Now(), which made ParseWhen impure despite documenting
// otherwise and made "01/02" untestable — the assertion below would have
// depended on the year the suite happened to run in. Two far-apart injected
// years prove the default follows the argument.
func TestParseWhenYearlessSlashDateUsesInjectedNow(t *testing.T) {
	loc := helsinki(t)

	for _, year := range []int{2019, 2031} {
		t.Run(time.Month(1).String()+"-"+time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006"), func(t *testing.T) {
			now := time.Date(year, 6, 15, 12, 0, 0, 0, time.UTC)

			got, err := parseWhen("01/02", now, loc)
			if err != nil {
				t.Fatalf("ParseWhen(01/02) error = %v", err)
			}

			local := got.In(loc)
			if local.Year() != year {
				t.Errorf("ParseWhen(01/02) year = %d, want %d (the injected now's year)", local.Year(), year)
			}
			// Day-first still holds: 1 February, not 2 January.
			if local.Month() != time.February || local.Day() != 1 {
				t.Errorf("ParseWhen(01/02) = %v, want 1 February %d", local, year)
			}
		})
	}
}

// TestParseWhenYearlessSlashDateCrossesAYearBoundary: the same input resolved
// either side of new year lands in the respective years, which is only possible
// if the injected now supplies the default.
func TestParseWhenYearlessSlashDateCrossesAYearBoundary(t *testing.T) {
	loc := helsinki(t)

	// Midday, so both instants sit unambiguously inside their local year: at
	// 23:59 UTC on 31 December, Helsinki (UTC+2 in winter) is already 1 January.
	newYearsEve := time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC)
	newYearsDay := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)

	before, err := parseWhen("01/02", newYearsEve, loc)
	if err != nil {
		t.Fatalf("ParseWhen(01/02) on new year's eve error = %v", err)
	}
	after, err := parseWhen("01/02", newYearsDay, loc)
	if err != nil {
		t.Fatalf("ParseWhen(01/02) on new year's day error = %v", err)
	}

	if before.In(loc).Year() != 2026 {
		t.Errorf("year on 2026-12-31 = %d, want 2026", before.In(loc).Year())
	}
	if after.In(loc).Year() != 2027 {
		t.Errorf("year on 2027-01-01 = %d, want 2027", after.In(loc).Year())
	}
}

// TestParseWhenYearlessSlashDateUsesTheYearInLoc: the default year is the
// caller's local year, not now's UTC year. 2026-12-31 23:59 UTC is already
// 2027-01-01 in Helsinki, so a yearless date typed then means 2027 — a user's
// "01/02" is relative to the calendar they are looking at.
func TestParseWhenYearlessSlashDateUsesTheYearInLoc(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)

	if now.In(loc).Year() != 2027 {
		t.Fatalf("test premise drifted: 2026-12-31 23:59 UTC is year %d in Helsinki, expected 2027",
			now.In(loc).Year())
	}

	got, err := parseWhen("01/02", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(01/02) error = %v", err)
	}
	if year := got.In(loc).Year(); year != 2027 {
		t.Errorf("ParseWhen(01/02) year = %d, want 2027 (now's year IN loc, not in UTC)", year)
	}
}

// TestParseWhenSlashDateWithTimeHonoursTheTime: "01.02.2026 18:00" carries both
// a date and a wall-clock time, and both must survive. The time half was
// parsed into locals that the function then discarded through a dead
// `_ = haveTime`; this pins the behaviour the parse actually has.
func TestParseWhenSlashDateWithTimeHonoursTheTime(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	got, err := parseWhen("01.02.2026 18:30", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(01.02.2026 18:30) error = %v", err)
	}

	local := got.In(loc)
	if local.Hour() != 18 || local.Minute() != 30 {
		t.Errorf("ParseWhen(01.02.2026 18:30) local time = %02d:%02d, want 18:30", local.Hour(), local.Minute())
	}
	if local.Year() != 2026 || local.Month() != time.February || local.Day() != 1 {
		t.Errorf("ParseWhen(01.02.2026 18:30) date = %v, want 1 February 2026", local)
	}
}

// TestParseWhenNaturalLanguage: a supported natural-language form resolves to a
// plausible future instant. "in 3 days" is deterministic relative to now, so the
// day is asserted; it must also be strictly after now.
func TestParseWhenNaturalLanguage(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	got, err := parseWhen("in 3 days", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(in 3 days) error = %v", err)
	}
	if !got.After(now) {
		t.Errorf("ParseWhen(in 3 days) = %v, want an instant after now %v", got, now)
	}
	// now + 3 days lands on 25 August 2026 regardless of the time-of-day rule
	// the parser applies, because 12:00 UTC + 72h stays inside that day in loc.
	if wantDay := now.Add(72 * time.Hour).In(loc).Day(); got.In(loc).Day() != wantDay {
		t.Errorf("ParseWhen(in 3 days) local day = %d, want %d", got.In(loc).Day(), wantDay)
	}
}

// TestParseWhenRefusesGibberish is the refusal boundary: pure gibberish must
// return an error, not a guessed time. A parser that invents a time here fires
// reminders at random.
func TestParseWhenRefusesGibberish(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	got, err := parseWhen("asdfqwer", now, loc)
	if err == nil {
		t.Errorf("ParseWhen(asdfqwer) = %v, want error (must not guess)", got)
	}
}

// TestParseWhenPastAbsoluteParses: a past absolute datetime PARSES. Rejecting a
// past time is the handler's job, not the parser's, so this asserts only that it
// resolves without error and lands before now.
func TestParseWhenPastAbsoluteParses(t *testing.T) {
	loc := helsinki(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	got, err := parseWhen("2020-01-01 09:00", now, loc)
	if err != nil {
		t.Fatalf("ParseWhen(past absolute) error = %v, want nil (parser does not reject past)", err)
	}
	if !got.Before(now) {
		t.Errorf("ParseWhen(2020-01-01 09:00) = %v, want an instant before now %v", got, now)
	}
}
