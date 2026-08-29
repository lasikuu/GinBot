package trigger

import (
	"regexp"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

func fixedRoller(n int) Roller {
	return func(int) int { return n }
}

func compilePattern(t *testing.T, phrase string, mode pb.TriggerMode) *regexp.Regexp {
	t.Helper()
	re, err := Compile(phrase, mode)
	if err != nil {
		t.Fatalf("Compile(%q, %v): %v", phrase, mode, err)
	}
	return re
}

func TestSelectExactWinsOverAny(t *testing.T) {
	candidates := []Candidate{
		{ID: "any-1", Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Pattern: compilePattern(t, "hello", pb.TriggerMode_TRIGGER_MODE_ANY)},
		{ID: "exact-1", Mode: pb.TriggerMode_TRIGGER_MODE_EXACT, Pattern: compilePattern(t, "hello", pb.TriggerMode_TRIGGER_MODE_EXACT)},
	}

	got := Select("hello", candidates, fixedRoller(0))
	if got == nil {
		t.Fatal("Select returned nil, want the exact candidate")
	}
	if got.ID != "exact-1" {
		t.Errorf("Select chose %q, want the exact candidate %q", got.ID, "exact-1")
	}
}

func TestSelectNilWhenNothingMatches(t *testing.T) {
	candidates := []Candidate{
		{ID: "any-1", Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Pattern: compilePattern(t, "goodbye", pb.TriggerMode_TRIGGER_MODE_ANY)},
	}

	if got := Select("hello there", candidates, fixedRoller(0)); got != nil {
		t.Errorf("Select = %+v, want nil", got)
	}
}

func TestSelectNilForMessageEmptyAfterSpoilerStripping(t *testing.T) {
	candidates := []Candidate{
		{ID: "any-1", Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Pattern: compilePattern(t, "secret", pb.TriggerMode_TRIGGER_MODE_ANY)},
	}

	got := Select("||secret||", candidates, fixedRoller(0))
	if got != nil {
		t.Errorf("Select(%q) = %+v, want nil (message is empty after stripping spoilers)", "||secret||", got)
	}
}

func TestSelectSkipsNilPatternWithoutPanicking(t *testing.T) {
	candidates := []Candidate{
		{ID: "broken", Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Pattern: nil},
		{ID: "good", Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Pattern: compilePattern(t, "hello", pb.TriggerMode_TRIGGER_MODE_ANY)},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Select panicked on a nil Pattern: %v", r)
		}
	}()

	got := Select("hello world", candidates, fixedRoller(0))
	if got == nil {
		t.Fatal("Select returned nil, want the candidate with the non-nil pattern")
	}
	if got.ID != "good" {
		t.Errorf("Select chose %q, want %q", got.ID, "good")
	}
}

func TestSelectOnlyConsidersFirstMaxCandidates(t *testing.T) {
	candidates := make([]Candidate, MaxCandidates+1)
	for i := range candidates {
		candidates[i] = Candidate{
			ID:      "filler",
			Mode:    pb.TriggerMode_TRIGGER_MODE_ANY,
			Pattern: compilePattern(t, "filler-phrase-not-in-message", pb.TriggerMode_TRIGGER_MODE_ANY),
		}
	}
	candidates[MaxCandidates] = Candidate{
		ID:      "past-the-cutoff",
		Mode:    pb.TriggerMode_TRIGGER_MODE_ANY,
		Pattern: compilePattern(t, "hello", pb.TriggerMode_TRIGGER_MODE_ANY),
	}

	got := Select("hello world", candidates, fixedRoller(0))
	if got != nil {
		t.Errorf("Select matched %+v from beyond MaxCandidates (%d); it must not be considered", got, MaxCandidates)
	}
}

func TestSelectRollOutOfRangeDoesNotPanic(t *testing.T) {
	candidates := []Candidate{
		{ID: "only-match", Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Pattern: compilePattern(t, "hello", pb.TriggerMode_TRIGGER_MODE_ANY)},
	}

	for _, badRoll := range []int{-1, 1, 100, -100} {
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Select panicked on an out-of-range roll %d: %v", badRoll, r)
				}
			}()

			got := Select("hello world", candidates, fixedRoller(badRoll))
			if got == nil {
				t.Fatal("Select returned nil, want the only candidate (out-of-range roll should fall back to index 0)")
			}
			if got.ID != "only-match" {
				t.Errorf("Select chose %q, want %q", got.ID, "only-match")
			}
		})
	}
}

func TestSelectReturnsACopyNotAPointerIntoTheSlice(t *testing.T) {
	candidates := []Candidate{
		{ID: "original", Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Pattern: compilePattern(t, "hello", pb.TriggerMode_TRIGGER_MODE_ANY)},
	}

	got := Select("hello world", candidates, fixedRoller(0))
	if got == nil {
		t.Fatal("Select returned nil")
	}

	got.ID = "mutated"

	if candidates[0].ID != "original" {
		t.Errorf("mutating the returned candidate changed the caller's slice: %q", candidates[0].ID)
	}
}

func TestFiresThreshold(t *testing.T) {
	c := Candidate{Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Chance: 10}

	for roll := range 10 {
		if !Fires(c, fixedRoller(roll)) {
			t.Errorf("Fires with roll=%d = false, want true (chance 10)", roll)
		}
	}
	for roll := 10; roll < 100; roll += 7 {
		if Fires(c, fixedRoller(roll)) {
			t.Errorf("Fires with roll=%d = true, want false (chance 10)", roll)
		}
	}
}

func TestFiresExactMultiplierShiftsTheBoundary(t *testing.T) {
	const stored = int32(10)
	any := Candidate{Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Chance: stored}
	exact := Candidate{Mode: pb.TriggerMode_TRIGGER_MODE_EXACT, Chance: stored}

	if Fires(any, fixedRoller(20)) {
		t.Error("any-mode Fires at roll=20 = true, want false (effective chance 10)")
	}
	if !Fires(exact, fixedRoller(20)) {
		t.Error("exact-mode Fires at roll=20 = false, want true (effective chance 30)")
	}
}

func TestFiresAtEffectiveChance100AlwaysFires(t *testing.T) {
	c := Candidate{Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Chance: 100}

	for roll := range int(MaxChance) {
		if !Fires(c, fixedRoller(roll)) {
			t.Fatalf("Fires with roll=%d = false, want true (effective chance 100)", roll)
		}
	}
}
