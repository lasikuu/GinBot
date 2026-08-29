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
