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
// forms; an input we cannot understand is refused, never approximated.
var ErrNoMatch = errors.New("could not understand the time")

const (
	unitSecond = time.Second
	unitMinute = time.Minute
	unitHour   = time.Hour
	unitDay    = 24 * time.Hour
	unitWeek   = 7 * unitDay
	unitMonth  = 30 * unitDay // calendar-month approximation
)

// ParseWhen resolves a human-supplied "when" into a UTC instant, trying
// duration, then absolute datetime, then natural language — least ambiguous
// first. Zoneless wall-clock times and relative phrases resolve in loc, which
// defaults to UTC when nil.
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

// ParseDuration parses a composed duration such as "4M2d8h30s". Units are
// M=month (30d), w, d, h, m=minute, s; case matters, each may appear once, in
// any order, and the total must be positive. No fractions, signs or bare
// numbers, so dates and phrases fall through to the later parsers.
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

		// Checked as we go: the input length bounds nothing.
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

// absoluteLayouts are the accepted ISO-ish forms, tried in order. A layout
// carrying its own offset keeps it; a zoneless one is read in the caller's loc.
var absoluteLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseAbsolute parses an absolute datetime. ISO year-first forms are
// unambiguous; ambiguous numeric dates are resolved day-first (little-endian),
// so "01/02" is 1 February.
func parseAbsolute(input string, loc *time.Location, now time.Time) (time.Time, bool) {
	for _, layout := range absoluteLayouts {
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
// "01.02.2026 18:00", separated by "/" or ".". A yearless date defaults to the
// year of now in loc.
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
		// Two-digit years are read as 2000+yy.
		if y < 100 {
			y += 2000
		}
		year = y
	}

	if month < 1 || month > 12 || day < 1 || day > 31 || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return time.Time{}, false
	}

	t := time.Date(year, time.Month(month), day, hour, minute, 0, 0, loc)
	// time.Date normalises out-of-range days (32 Jan -> 1 Feb); reject instead.
	if t.Day() != day || int(t.Month()) != month {
		return time.Time{}, false
	}

	return t, true
}

// parseNatural resolves an English phrase via github.com/olebedev/when against
// now-in-loc, so "tomorrow" means the caller's tomorrow.
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

// atoi parses a non-negative integer, reporting failure instead of erroring so
// a malformed date field falls through to the next parser.
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
