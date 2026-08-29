package trigger

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// EffectiveChance resolves a stored chance into a firing percentage; 0 and any
// negative mean DefaultChance.
func EffectiveChance(stored int32, mode pb.TriggerMode) int32 {
	if stored < 0 {
		stored = 0
	}
	if stored == 0 {
		stored = DefaultChance
	}

	if mode == pb.TriggerMode_TRIGGER_MODE_EXACT {
		stored *= ExactChanceMultiplier
	}

	if stored > MaxChance {
		stored = MaxChance
	}

	return stored
}
