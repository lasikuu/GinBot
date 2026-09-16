package reminder

import (
	"testing"
	"time"
)

var (
	validateCron        = ValidateCron
	nextOccurrence      = NextOccurrence
	nextOccurrenceAfter = NextOccurrenceAfter
)

func tokyo(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("load Asia/Tokyo: %v", err)
	}
	return loc
}

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

// @every carries no calendar of its own — ConstantDelaySchedule.Next is a bare
// t.Add(delay) — so only the anchor decides which weekday a weekly repeat lands on.
func TestNextOccurrenceAfterEveryWeekKeepsWeekdayAndTime(t *testing.T) {
	loc := tokyo(t)
	anchor := time.Date(2026, 6, 5, 21, 0, 0, 0, loc) // a Friday
	if anchor.Weekday() != time.Friday {
		t.Fatalf("test fixture broken: %v is not a Friday", anchor)
	}
	now := anchor.Add(5 * time.Second)

	got, phaseLost, err := nextOccurrenceAfter("@every 168h", anchor, now, loc)
	if err != nil {
		t.Fatalf("NextOccurrenceAfter: %v", err)
	}
	if phaseLost {
		t.Error("phaseLost = true for a fresh anchor")
	}

	want := anchor.AddDate(0, 0, 7)
	if !got.Equal(want) {
		t.Errorf("NextOccurrenceAfter = %v, want %v (one week after the anchor)", got, want)
	}
}

// A confirmation arriving days late, on an unrelated weekday, must not drag the
// reminder onto that weekday: this is the reported production failure.
func TestNextOccurrenceAfterSurvivesADelayedConfirmationOnADifferentWeekday(t *testing.T) {
	loc := tokyo(t)
	anchor := time.Date(2020, 1, 3, 21, 0, 0, 0, loc) // a Friday
	if anchor.Weekday() != time.Friday {
		t.Fatalf("test fixture broken: %v is not a Friday", anchor)
	}
	now := time.Date(2026, 6, 2, 14, 33, 0, 0, loc) // a Tuesday afternoon
	if now.Weekday() == time.Friday {
		t.Fatalf("test fixture broken: %v must not itself be a Friday", now)
	}

	got, phaseLost, err := nextOccurrenceAfter("@every 168h", anchor, now, loc)
	if err != nil {
		t.Fatalf("NextOccurrenceAfter: %v", err)
	}
	if phaseLost {
		t.Error("phaseLost = true; a six-year-old @every anchor is still exactly computable")
	}

	if !got.After(now) {
		t.Fatalf("NextOccurrenceAfter = %v, want strictly after %v", got, now)
	}
	if got.Weekday() != time.Friday {
		t.Errorf("weekday = %v, want Friday: the delayed confirmation moved the reminder's weekday", got.Weekday())
	}
	if h, m, s := got.Clock(); h != 21 || m != 0 || s != 0 {
		t.Errorf("clock = %02d:%02d:%02d, want 21:00:00: the delayed confirmation moved the reminder's clock time", h, m, s)
	}
}

// The scheduler re-claims anything at or before now on its next one-second
// tick, so overshooting is harmless but landing short floods the owner.
func TestNextOccurrenceAfterCatchesUpExactlyOnce(t *testing.T) {
	loc := tokyo(t)
	anchor := time.Date(2026, 5, 15, 21, 0, 0, 0, loc)
	now := time.Date(2026, 6, 5, 3, 0, 0, 0, loc)

	got, _, err := nextOccurrenceAfter("@every 168h", anchor, now, loc)
	if err != nil {
		t.Fatalf("NextOccurrenceAfter: %v", err)
	}

	want := time.Date(2026, 6, 5, 21, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("NextOccurrenceAfter = %v, want %v: the first occurrence after now, not a later one", got, want)
	}
}

func TestNextOccurrenceAfterNeverReturnsAtOrBeforeNowOrTheAnchorItself(t *testing.T) {
	loc := time.UTC
	anchor := time.Date(2026, 6, 15, 9, 0, 0, 0, loc)

	tests := []struct {
		name string
		spec string
		now  time.Time
	}{
		{name: "calendar spec, anchor equals now", spec: "0 9 * * *", now: anchor},
		{name: "calendar spec, anchor after now", spec: "0 9 * * *", now: anchor.Add(-time.Hour)},
		{name: "@every, anchor equals now", spec: "@every 1h", now: anchor},
		{name: "@every, anchor after now", spec: "@every 1h", now: anchor.Add(-time.Minute)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := nextOccurrenceAfter(tt.spec, anchor, tt.now, loc)
			if err != nil {
				t.Fatalf("NextOccurrenceAfter: %v", err)
			}
			if !got.After(tt.now) {
				t.Errorf("NextOccurrenceAfter = %v, want strictly after now %v", got, tt.now)
			}
			if got.Equal(anchor) {
				t.Error("NextOccurrenceAfter returned the anchor itself")
			}
		})
	}
}

// Helsinki goes EET(+2) -> EEST(+3) on 2026-03-29.
func TestNextOccurrenceAfterKeepsDSTBehaviorSpringForward(t *testing.T) {
	loc := helsinki(t)
	anchor := time.Date(2026, 3, 20, 9, 0, 0, 0, loc)
	now := time.Date(2026, 3, 28, 9, 30, 0, 0, loc)

	got, _, err := nextOccurrenceAfter("0 9 * * *", anchor, now, loc)
	if err != nil {
		t.Fatalf("NextOccurrenceAfter (spring): %v", err)
	}

	wantLocal := time.Date(2026, 3, 29, 9, 0, 0, 0, loc)
	wantUTC := time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC)
	if !got.Equal(wantLocal) {
		t.Errorf("NextOccurrenceAfter (spring) = %v, want %v (09:00 EEST)", got, wantLocal)
	}
	if !got.UTC().Equal(wantUTC) {
		t.Errorf("NextOccurrenceAfter (spring) UTC = %v, want %v", got.UTC(), wantUTC)
	}
}

// The mirror case, EEST(+3) -> EET(+2) on 2026-10-25.
func TestNextOccurrenceAfterKeepsDSTBehaviorFallBack(t *testing.T) {
	loc := helsinki(t)
	anchor := time.Date(2026, 10, 15, 9, 0, 0, 0, loc)
	now := time.Date(2026, 10, 24, 9, 30, 0, 0, loc)

	got, _, err := nextOccurrenceAfter("0 9 * * *", anchor, now, loc)
	if err != nil {
		t.Fatalf("NextOccurrenceAfter (fall): %v", err)
	}

	wantLocal := time.Date(2026, 10, 25, 9, 0, 0, 0, loc)
	wantUTC := time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC)
	if !got.Equal(wantLocal) {
		t.Errorf("NextOccurrenceAfter (fall) = %v, want %v (09:00 EET)", got, wantLocal)
	}
	if !got.UTC().Equal(wantUTC) {
		t.Errorf("NextOccurrenceAfter (fall) UTC = %v, want %v", got.UTC(), wantUTC)
	}
}

// The legacy import wrote @every rows without applying the interval floors, so
// a sub-minute repeat decades stale is reachable. Walking it one period at a
// time would be ~19 million steps and would surrender the phase to the bound.
func TestNextOccurrenceAfterKeepsPhaseOnADecadesStaleEverySchedule(t *testing.T) {
	loc := time.UTC
	anchor := time.Date(1990, 1, 1, 0, 0, 30, 0, loc)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, loc)

	got, phaseLost, err := nextOccurrenceAfter("@every 1m", anchor, now, loc)
	if err != nil {
		t.Fatalf("NextOccurrenceAfter: %v", err)
	}
	if phaseLost {
		t.Error("phaseLost = true; an @every anchor is computable at any distance")
	}
	if !got.After(now) {
		t.Fatalf("NextOccurrenceAfter = %v, want strictly after %v", got, now)
	}
	if want := time.Date(2026, 6, 15, 12, 0, 30, 0, loc); !got.Equal(want) {
		t.Errorf("NextOccurrenceAfter = %v, want %v: the anchor's 30-second offset must survive", got, want)
	}
}

// A calendar spec cannot be jumped arithmetically, so only the step bound
// protects the request handler. maxScheduleAdvanceSteps of "* * * * *" is three
// days; a month-stale anchor must give up the phase rather than keep walking.
func TestNextOccurrenceAfterAbandonsThePhaseBeyondTheStepBound(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, loc)
	anchor := now.AddDate(0, 0, -30)

	got, phaseLost, err := nextOccurrenceAfter("* * * * *", anchor, now, loc)
	if err != nil {
		t.Fatalf("NextOccurrenceAfter: %v", err)
	}
	if !phaseLost {
		t.Errorf("phaseLost = false for an anchor %d steps stale; the bound is %d",
			30*24*60, maxScheduleAdvanceSteps)
	}
	if want := now.Add(time.Minute); !got.Equal(want) {
		t.Errorf("NextOccurrenceAfter = %v, want %v: the fallback anchors on now", got, want)
	}
}

// Just inside the bound the phase is kept, which is what makes the test above
// an assertion about the bound rather than about staleness in general.
func TestNextOccurrenceAfterKeepsThePhaseInsideTheStepBound(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, loc)
	anchor := now.Add(-(maxScheduleAdvanceSteps - 1) * time.Minute)

	got, phaseLost, err := nextOccurrenceAfter("* * * * *", anchor, now, loc)
	if err != nil {
		t.Fatalf("NextOccurrenceAfter: %v", err)
	}
	if phaseLost {
		t.Error("phaseLost = true one step inside the bound")
	}
	if want := now.Add(time.Minute); !got.Equal(want) {
		t.Errorf("NextOccurrenceAfter = %v, want %v", got, want)
	}
}

// Matches NextOccurrence's contract: a spec that stops firing after robfig's
// five-year search is an error, not a zero time.
func TestNextOccurrenceAfterReportsAScheduleThatNeverFiresAgain(t *testing.T) {
	loc := time.UTC
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)

	// Feb 30th never occurs.
	if _, _, err := nextOccurrenceAfter("0 0 30 2 *", anchor, anchor, loc); err == nil {
		t.Error("NextOccurrenceAfter(never-firing spec) = nil error, want one")
	}
}

// The arithmetic jump replaces repeated ConstantDelaySchedule.Next calls, so it
// must agree with them exactly, including the sub-second truncation the first
// call applies and later ones do not.
func TestAdvanceByDelayMatchesSteppingTheSchedule(t *testing.T) {
	specs := []string{"@every 1m", "@every 90s", "@every 12h", "@every 168h"}
	anchors := []time.Time{
		time.Date(2026, 6, 5, 21, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 5, 21, 0, 30, 0, time.UTC),
		time.Date(2026, 6, 5, 21, 0, 0, 500_000_000, time.UTC),
	}
	offsets := []time.Duration{-time.Nanosecond, 0, time.Second, 47 * time.Hour, 5000 * time.Hour}

	for _, spec := range specs {
		schedule, err := parseCron(spec)
		if err != nil {
			t.Fatalf("parseCron(%q): %v", spec, err)
		}
		delay, fixed := constantDelay(schedule)
		if !fixed {
			t.Fatalf("parseCron(%q) is not a ConstantDelaySchedule", spec)
		}

		for _, anchor := range anchors {
			for _, offset := range offsets {
				now := anchor.Add(offset)

				var want time.Time
				for step := anchor; ; step = want {
					want = schedule.Next(step)
					if want.After(now) {
						break
					}
				}

				got, ok := advanceByDelay(anchor, now, delay)
				if !ok {
					t.Errorf("advanceByDelay(%v, %v, %v) refused; stepping reaches %v", anchor, now, delay, want)
					continue
				}
				if !got.Equal(want) {
					t.Errorf("advanceByDelay(%v, %v, %v) = %v, want %v from stepping", anchor, now, delay, got, want)
				}
			}
		}
	}
}
