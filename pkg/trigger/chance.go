package trigger

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

// EffectiveChance resolves a stored chance into the percentage a trigger fires
// at.
//
// A stored 0 means DefaultChance, preserving the old bot's semantics where the
// column doubled as "unset". Exact mode is then multiplied by
// ExactChanceMultiplier, and the result is clamped to MaxChance. A negative
// stored value is treated as 0; the column has a CHECK that forbids it, but the
// function must not return nonsense if one appears.
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
