package trigger

import (
	"regexp"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// Roller returns a pseudo-random integer in [0,n). It is injected so that
// selection and chance rolls are deterministic under test.
type Roller func(n int) int

// Candidate is one compiled trigger eligible for matching.
type Candidate struct {
	ID      string
	Mode    pb.TriggerMode
	Chance  int32
	Pattern *regexp.Regexp
}

// Select picks the candidate a message resolves to, before any chance roll, or
// nil when nothing matches.
//
// An exact match wins and discards every non-exact candidate, which is what
// makes an exact phrase worth registering. Otherwise one of the matching
// candidates is chosen at random.
func Select(message string, candidates []Candidate, roll Roller) *Candidate {
	normalised := StripSpoilers(message)
	if normalised == "" {
		return nil
	}

	limit := len(candidates)
	if limit > MaxCandidates {
		limit = MaxCandidates
	}

	var matched []Candidate
	var exactMatched []Candidate
	for _, candidate := range candidates[:limit] {
		if candidate.Pattern == nil {
			continue
		}
		if !candidate.Pattern.MatchString(normalised) {
			continue
		}

		matched = append(matched, candidate)
		if candidate.Mode == pb.TriggerMode_TRIGGER_MODE_EXACT {
			exactMatched = append(exactMatched, candidate)
		}
	}

	if len(exactMatched) > 0 {
		matched = exactMatched
	}
	if len(matched) == 0 {
		return nil
	}

	idx := roll(len(matched))
	if idx < 0 || idx >= len(matched) {
		// A caller's bad Roller must not panic the hot path.
		idx = 0
	}

	// Return a copy: never a pointer into the caller's slice, which the cache
	// shares with every other reader.
	picked := matched[idx]
	return &picked
}

// Fires reports whether a selected candidate's chance roll succeeds.
func Fires(c Candidate, roll Roller) bool {
	return roll(int(MaxChance)) < int(EffectiveChance(c.Chance, c.Mode))
}
