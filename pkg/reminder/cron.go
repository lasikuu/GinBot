package reminder

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// Minimum repeat intervals.
//
// Carried over from the old bot: a repeat aimed at a public channel must be no
// more frequent than every 12 hours, while a direct message may repeat as often
// as every 10 minutes. The floor is about not spamming a shared space; a DM only
// bothers the one person who asked for it.
//
// The boundary is inclusive: a schedule whose gap is exactly the floor is
// allowed; only a strictly-more-frequent one is rejected.
const (
	MinIntervalPublic = 12 * time.Hour
	MinIntervalDM     = 10 * time.Minute
)

// parseCron accepts the standard 5-field crontab form, descriptors
// (@daily, @hourly, ...) and @every. It is the semantic gate the proto's
// shape-only regex cannot be: "99 99 99 99 99" matches the regex but fails here.
//
// cron.ParseStandard is used rather than a hand-built cron.NewParser because it
// already enables exactly this option set (Minute|Hour|Dom|Month|Dow|Descriptor)
// and, unlike a bare NewParser, also accepts @every.
//
// @every is NOT there to express sub-minute repeats: ValidateRepeatInterval
// rejects anything below the destination's floor — 10 minutes for a DM, 12 hours
// for a shared channel — so `@every 90s` parses here and is then refused, and no
// sub-minute repeat is reachable through the product at all. What @every buys is
// fixed intervals a 5-field crontab cannot state, such as `@every 90m` or
// `@every 36h`. That is why repeat_cron alone is enough and the schema carries no
// separate interval column: every schedule the product allows is expressible as a
// cron string.
func parseCron(spec string) (cron.Schedule, error) {
	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid repeat schedule: %w", err)
	}

	return schedule, nil
}

// ValidateCron reports whether a repeat_cron string is semantically valid.
//
// The proto validates only the shape of the string; this validates its meaning.
// A caller (CreateReminder, UpdateReminder) maps a non-nil return to
// codes.InvalidArgument.
func ValidateCron(spec string) error {
	_, err := parseCron(spec)
	return err
}

// minInterval returns the minimum permitted gap between occurrences given
// whether the reminder targets a direct message. A DM gets the lower floor;
// everything else (a public channel) gets the higher one.
func minInterval(isDM bool) time.Duration {
	if isDM {
		return MinIntervalDM
	}
	return MinIntervalPublic
}

// intervalSampleBase is the fixed instant the shortest-gap sample is measured
// from. Sampling the next two occurrences needs a base; a fixed, DST-free UTC
// instant keeps the check deterministic and independent of the wall clock.
var intervalSampleBase = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ValidateRepeatInterval checks both that a repeat schedule parses and that it
// is not more frequent than the applicable floor (12h for a channel, 10m for a
// DM).
//
// The shortest gap is APPROXIMATED by sampling the next two occurrences from a
// fixed base and measuring the gap between them. This is exact for
// fixed-interval schedules (@every, "0 * * * *") and for the common regular
// crontab; it can under- or over-estimate for irregular schedules whose gaps
// vary (e.g. "0 0,1 * * *" fires twice an hour apart then waits 23h), but
// sampling the immediate next gap is what the old bot did and is a conservative,
// cheap check that catches the abuse case — a schedule that fires far too often
// — without a full occurrence walk. The boundary is inclusive.
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

// NextOccurrence returns the next fire time strictly after `after`, evaluated in
// loc.
//
// The schedule is evaluated in the reminder's own timezone so that a daily
// "0 9 * * *" means 9am local, not 9am UTC — which matters across a DST
// transition, where a naive UTC computation drifts by an hour. The returned
// time is expressed in loc; callers store its UTC instant. A nil loc is treated
// as UTC. It errors when the schedule does not parse or never fires again.
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
