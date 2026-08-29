package trigger

import (
	"regexp"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

type Roller func(n int) int

type Candidate struct {
	ID      string
	Mode    pb.TriggerMode
	Chance  int32
	Pattern *regexp.Regexp
}

// Select picks the candidate a message resolves to before any chance roll, or
// nil when nothing matches. Any exact match discards every non-exact one;
// otherwise the pick is random.
func Select(message string, candidates []Candidate, roll Roller) *Candidate {
	normalised := StripSpoilers(message)
	if normalised == "" {
		return nil
	}

	limit := min(len(candidates), MaxCandidates)

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

	picked := matched[idx]
	return &picked
}

func Fires(c Candidate, roll Roller) bool {
	return roll(int(MaxChance)) < int(EffectiveChance(c.Chance, c.Mode))
}
