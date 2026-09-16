package reminder

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// Minimum repeat intervals, inclusive: a gap exactly at the floor is allowed.
const (
	MinIntervalPublic = 12 * time.Hour
	MinIntervalDM     = 10 * time.Minute
)

// parseCron accepts the standard 5-field crontab form, descriptors and @every.
// It is the semantic gate the proto's shape-only regex cannot be: "99 99 99 99
// 99" matches the regex but fails here.
func parseCron(spec string) (cron.Schedule, error) {
	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid repeat schedule: %w", err)
	}

	return schedule, nil
}

// ValidateCron reports whether a repeat_cron string is semantically valid.
func ValidateCron(spec string) error {
	_, err := parseCron(spec)
	return err
}

func minInterval(isDM bool) time.Duration {
	if isDM {
		return MinIntervalDM
	}
	return MinIntervalPublic
}

// intervalSampleBase is fixed and DST-free so the gap sample is deterministic.
var intervalSampleBase = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ValidateRepeatInterval rejects a schedule more frequent than its floor. The
// gap is approximated from the first two occurrences after intervalSampleBase,
// which is exact for fixed intervals and only approximate for irregular ones
// (e.g. "0 0,1 * * *" fires an hour apart then waits 23h).
func ValidateRepeatInterval(spec string, isDM bool) error {
	schedule, err := parseCron(spec)
	if err != nil {
		return err
	}

	first := schedule.Next(intervalSampleBase)
	if first.IsZero() {
		return fmt.Errorf("repeat schedule never fires")
	}
	second := schedule.Next(first)
	if second.IsZero() {
		return fmt.Errorf("repeat schedule fires only once")
	}

	gap := second.Sub(first)
	floor := minInterval(isDM)
	if gap < floor {
		return fmt.Errorf("repeat is too frequent: minimum interval is %s, got %s", floor, gap)
	}

	return nil
}

// NextOccurrence returns the next fire time strictly after `after`, expressed
// in loc (UTC when nil). Evaluating in loc is what keeps "0 9 * * *" at 9am
// local across a DST transition instead of drifting an hour.
func NextOccurrence(spec string, after time.Time, loc *time.Location) (time.Time, error) {
	schedule, err := parseCron(spec)
	if err != nil {
		return time.Time{}, err
	}
	if loc == nil {
		loc = time.UTC
	}

	next := schedule.Next(after.In(loc))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("repeat schedule never fires again")
	}

	return next.In(loc), nil
}

// maxScheduleAdvanceSteps bounds the calendar walk. Only specs that cleared
// ValidateRepeatInterval reach it, so at the 10-minute DM floor it spans a
// month of staleness; @every takes the arithmetic path and never steps.
const maxScheduleAdvanceSteps = 30 * 24 * 6

// NextOccurrenceAfter returns the first fire time strictly after now, advancing
// from anchor rather than from now so a late confirmation cannot permanently
// shift a repeat's phase; a missed window yields one catch-up. phaseLost
// reports that the anchor was unusable and now was substituted for it.
func NextOccurrenceAfter(spec string, anchor time.Time, now time.Time, loc *time.Location) (next time.Time, phaseLost bool, err error) {
	schedule, err := parseCron(spec)
	if err != nil {
		return time.Time{}, false, err
	}
	if loc == nil {
		loc = time.UTC
	}

	next, phaseLost, err = advanceSchedule(schedule, anchor.In(loc), now.In(loc))
	if err != nil {
		return time.Time{}, false, err
	}

	return next.In(loc), phaseLost, nil
}

func advanceSchedule(schedule cron.Schedule, anchor time.Time, now time.Time) (time.Time, bool, error) {
	// An @every schedule is a bare t.Add(delay) with no calendar of its own, so
	// its occurrences are arithmetic. Stepping them one period at a time would
	// exhaust the bound below on an ordinary outage, because the legacy import
	// produced @every rows without applying the interval floors.
	if delay, fixed := constantDelay(schedule); fixed {
		if next, ok := advanceByDelay(anchor, now, delay); ok {
			return next, false, nil
		}

		return advanceFromNow(schedule, now)
	}

	next := anchor
	for range maxScheduleAdvanceSteps {
		next = schedule.Next(next)
		if next.IsZero() {
			return time.Time{}, false, fmt.Errorf("repeat schedule never fires again")
		}
		if next.After(now) {
			return next, false, nil
		}
	}

	return advanceFromNow(schedule, now)
}

// advanceFromNow abandons the anchor's phase. A reminder that fires on the
// wrong day beats one stuck behind an anchor too stale to advance from.
func advanceFromNow(schedule cron.Schedule, now time.Time) (time.Time, bool, error) {
	next := schedule.Next(now)
	if next.IsZero() {
		return time.Time{}, false, fmt.Errorf("repeat schedule never fires again")
	}

	return next, true, nil
}

func constantDelay(schedule cron.Schedule) (time.Duration, bool) {
	fixed, ok := schedule.(cron.ConstantDelaySchedule)
	if !ok || fixed.Delay <= 0 {
		return 0, false
	}

	return fixed.Delay, true
}

// advanceByDelay jumps straight to the first occurrence after now. The initial
// step mirrors ConstantDelaySchedule.Next, which discards the sub-second part
// of its argument; every later one lands on a whole multiple of delay.
func advanceByDelay(anchor time.Time, now time.Time, delay time.Duration) (time.Time, bool) {
	first := anchor.Add(delay - time.Duration(anchor.Nanosecond()))
	if first.After(now) {
		return first, true
	}

	next := first.Add((now.Sub(first)/delay + 1) * delay)
	// Sub saturates and the multiplication can overflow on an absurd anchor.
	// Either way a non-future result means the arithmetic cannot be trusted.
	if !next.After(now) {
		return time.Time{}, false
	}

	return next, true
}
