package reminder

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olebedev/when"
	"github.com/olebedev/when/rules/common"
	"github.com/olebedev/when/rules/en"
)

// ErrNoMatch is returned when an input matches none of the three supported
// forms. Callers map it to codes.InvalidArgument. It is never a silent guess: an
// input we cannot understand is refused, not approximated.
var ErrNoMatch = errors.New("could not understand the time")

// Duration-unit approximations.
//
// Go's time.ParseDuration knows only h/m/s, so the day/week/month units are
// hand-rolled here. Week and day are exact. Month is an APPROXIMATION: a
// calendar month has no fixed length, so "1M" is treated as 30 days. This is
// deliberate and documented — a reminder "in 3M" lands 90 days out, which is
// close enough for a relative reminder and avoids dragging calendar arithmetic
// into a duration string.
const (
	unitSecond = time.Second
	unitMinute = time.Minute
	unitHour   = time.Hour
	unitDay    = 24 * time.Hour
	unitWeek   = 7 * unitDay
	unitMonth  = 30 * unitDay // calendar-month approximation
)

// ParseWhen resolves a human-supplied "when" into a concrete UTC instant.
//
// It tries the three supported forms in order, cheapest and least ambiguous
// first:
//
//  1. Duration — e.g. "4M2d8h30s", relative to now.
//  2. Absolute datetime — ISO-ish forms, day-first for slash dates.
//  3. Natural language — via github.com/olebedev/when.
//
// loc is the caller's timezone. It matters for the absolute form (a zoneless
// wall-clock time is interpreted in loc, then converted to UTC) and for the
// natural-language form (relative phrases resolve against now-in-loc). A nil loc
// is treated as UTC.
//
// now is injected rather than read from the clock so the resolution is a pure
// function of its inputs and can be tested deterministically.
//
// The returned time is always UTC.
func ParseWhen(input string, now time.Time, loc *time.Location) (time.Time, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return time.Time{}, fmt.Errorf("%w: empty input", ErrNoMatch)
	}
	if loc == nil {
		loc = time.UTC
	}

	if d, err := ParseDuration(input); err == nil {
		return now.Add(d).UTC(), nil
	}

	if t, ok := parseAbsolute(input, loc, now); ok {
		return t.UTC(), nil
	}

	if t, ok := parseNatural(input, loc, now); ok {
		return t.UTC(), nil
	}

	return time.Time{}, fmt.Errorf("%w: %q", ErrNoMatch, input)
}

// ParseDuration parses a composed duration such as "4M2d8h30s".
//
// Units: M=month (30d), w=week, d=day, h=hour, m=minute, s=second. Case is
// significant: M is month, m is minute. Each unit may appear at most once, in
// any order. The total must be strictly positive.
//
// It is stricter than time.ParseDuration on purpose: no fractional numbers, no
// leading sign, no bare number without a unit. An input that is really a date or
// a phrase must fall through to the later parsers rather than be misread here.
func ParseDuration(input string) (time.Duration, error) {
	var total time.Duration
	seen := make(map[byte]bool, 6)

	i := 0
	for i < len(input) {
		start := i
		for i < len(input) && input[i] >= '0' && input[i] <= '9' {
			i++
		}
		if i == start {
			return 0, fmt.Errorf("missing number at %q", input[i:])
		}

		// A number can be arbitrarily long; guard the multiplication below
		// against overflow by parsing into int64 and checking as we go.
		var value int64
		for _, r := range input[start:i] {
			digit := int64(r - '0')
			if value > (int64(^uint64(0)>>1)-digit)/10 {
				return 0, fmt.Errorf("duration overflow")
			}
			value = value*10 + digit
		}

		if i >= len(input) {
			return 0, fmt.Errorf("number %d has no unit", value)
		}

		unit := input[i]
		i++

		var unitDur time.Duration
		switch unit {
		case 'M':
			unitDur = unitMonth
		case 'w':
			unitDur = unitWeek
		case 'd':
			unitDur = unitDay
		case 'h':
			unitDur = unitHour
		case 'm':
			unitDur = unitMinute
		case 's':
			unitDur = unitSecond
		default:
			return 0, fmt.Errorf("unknown unit %q", string(unit))
		}

		if seen[unit] {
			return 0, fmt.Errorf("unit %q repeated", string(unit))
		}
		seen[unit] = true

		// Overflow guard on the value*unit multiplication and the running total.
		if value != 0 && unitDur > time.Duration(int64(^uint64(0)>>1))/time.Duration(value) {
			return 0, fmt.Errorf("duration overflow")
		}
		part := time.Duration(value) * unitDur
		if part > 0 && total > time.Duration(int64(^uint64(0)>>1))-part {
			return 0, fmt.Errorf("duration overflow")
		}
		total += part
	}

	if total <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}

	return total, nil
}

// absoluteLayouts are the accepted ISO-ish absolute forms, tried in order.
//
// Both "T" and space separators are accepted, with and without seconds, with
// and without an explicit timezone offset. A form carrying its own offset is
// parsed with time.Parse and keeps that offset; a zoneless form is parsed with
// time.ParseInLocation against the caller's loc.
var absoluteLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseAbsolute parses an absolute datetime.
//
// ISO forms (year-first) are unambiguous and handled by absoluteLayouts.
// Slash/dotted numeric dates are ambiguous, and the rule — carried over from the
// old bot's endian_precedence [little, middle] — is DAY-FIRST (little-endian):
// "01/02" is 1 February, not 2 January. parseSlashDate applies that.
//
// now is threaded through only for parseSlashDate's year default.
func parseAbsolute(input string, loc *time.Location, now time.Time) (time.Time, bool) {
	for _, layout := range absoluteLayouts {
		// A layout that carries an offset is parsed in that offset; a zoneless
		// layout is interpreted in the caller's zone.
		if strings.Contains(layout, "Z07:00") {
			if t, err := time.Parse(layout, input); err == nil {
				return t, true
			}
			continue
		}
		if t, err := time.ParseInLocation(layout, input, loc); err == nil {
			return t, true
		}
	}

	return parseSlashDate(input, loc, now)
}

// parseSlashDate parses day-first numeric dates like "01/02", "1/2/2026" or
// "01.02.2026 18:00". Separators may be "/" or ".".
//
// This is the ambiguous case the day-first rule exists for. A two-field date has
// no year and defaults to the year of `now` in loc — the INJECTED now, not the
// wall clock, so ParseWhen stays the pure function it documents and "01/02" is
// testable across a year boundary.
func parseSlashDate(input string, loc *time.Location, now time.Time) (time.Time, bool) {
	datePart := input
	hour, minute := 0, 0

	if fields := strings.Fields(input); len(fields) == 2 {
		datePart = fields[0]
		hm := strings.SplitN(fields[1], ":", 2)
		if len(hm) != 2 {
			return time.Time{}, false
		}
		h, ok1 := atoi(hm[0])
		m, ok2 := atoi(hm[1])
		if !ok1 || !ok2 {
			return time.Time{}, false
		}
		hour, minute = h, m
	} else if len(fields) != 1 {
		return time.Time{}, false
	}

	sep := ""
	switch {
	case strings.Contains(datePart, "/"):
		sep = "/"
	case strings.Contains(datePart, "."):
		sep = "."
	default:
		return time.Time{}, false
	}

	parts := strings.Split(datePart, sep)
	if len(parts) != 2 && len(parts) != 3 {
		return time.Time{}, false
	}

	day, ok1 := atoi(parts[0])
	month, ok2 := atoi(parts[1])
	if !ok1 || !ok2 {
		return time.Time{}, false
	}

	year := now.In(loc).Year()
	if len(parts) == 3 {
		y, ok := atoi(parts[2])
		if !ok {
			return time.Time{}, false
		}
		// Two-digit years are read as 2000+yy; nobody sets a reminder in the
		// first century.
		if y < 100 {
			y += 2000
		}
		year = y
	}

	if month < 1 || month > 12 || day < 1 || day > 31 || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return time.Time{}, false
	}

	t := time.Date(year, time.Month(month), day, hour, minute, 0, 0, loc)
	// time.Date normalises out-of-range days (32 Jan -> 1 Feb); reject rather
	// than silently accept a date the user did not type.
	if t.Day() != day || int(t.Month()) != month {
		return time.Time{}, false
	}

	return t, true
}

// parseNatural resolves a natural-language phrase via github.com/olebedev/when.
//
// The English rule set is the base "common" rules plus the "en" rules, as the
// spec requires. A phrase that when cannot place returns no match and is
// refused by Parse — never guessed.
//
// when resolves relative phrases ("in 3 days", "tomorrow morning") against a
// base time; that base is now-in-loc so "tomorrow" means the caller's tomorrow.
func parseNatural(input string, loc *time.Location, now time.Time) (time.Time, bool) {
	w := when.New(nil)
	w.Add(en.All...)
	w.Add(common.All...)

	result, err := w.Parse(input, now.In(loc))
	if err != nil || result == nil {
		return time.Time{}, false
	}

	return result.Time, true
}

// atoi parses a non-negative integer, reporting failure rather than panicking,
// so a malformed date field falls through to the next parser instead of
// aborting.
func atoi(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}

	return n, true
}
