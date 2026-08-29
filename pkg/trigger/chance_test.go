package trigger

import (
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

func TestEffectiveChance(t *testing.T) {
	tests := []struct {
		name   string
		stored int32
		mode   pb.TriggerMode
		want   int32
	}{
		{"0 ANY", 0, pb.TriggerMode_TRIGGER_MODE_ANY, 5},
		{"0 EXACT", 0, pb.TriggerMode_TRIGGER_MODE_EXACT, 15},
		{"0 REGEX", 0, pb.TriggerMode_TRIGGER_MODE_REGEX, 5},
		{"0 UNSPECIFIED", 0, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED, 5},
		{"10 ANY", 10, pb.TriggerMode_TRIGGER_MODE_ANY, 10},
		{"10 EXACT", 10, pb.TriggerMode_TRIGGER_MODE_EXACT, 30},
		{"40 EXACT clamped", 40, pb.TriggerMode_TRIGGER_MODE_EXACT, 100},
		{"100 ANY", 100, pb.TriggerMode_TRIGGER_MODE_ANY, 100},
		{"100 EXACT clamped", 100, pb.TriggerMode_TRIGGER_MODE_EXACT, 100},
		{"1 EXACT", 1, pb.TriggerMode_TRIGGER_MODE_EXACT, 3},
		{"-5 ANY treated as 0", -5, pb.TriggerMode_TRIGGER_MODE_ANY, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveChance(tt.stored, tt.mode)
			if got != tt.want {
				t.Errorf("EffectiveChance(%d, %v) = %d, want %d", tt.stored, tt.mode, got, tt.want)
			}
		})
	}
}

func TestEffectiveChanceClampBoundary(t *testing.T) {
	tests := []struct {
		name   string
		stored int32
		want   int32
	}{
		{"33 exact, just under the clamp", 33, 99},
		{"34 exact, clamps to MaxChance", 34, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveChance(tt.stored, pb.TriggerMode_TRIGGER_MODE_EXACT)
			if got != tt.want {
				t.Errorf("EffectiveChance(%d, EXACT) = %d, want %d", tt.stored, got, tt.want)
			}
			if got > MaxChance {
				t.Errorf("EffectiveChance(%d, EXACT) = %d, exceeds MaxChance %d", tt.stored, got, MaxChance)
			}
		})
	}
}

func TestEffectiveChanceUnspecifiedBehavesAsAny(t *testing.T) {
	for _, stored := range []int32{0, 1, 10, 50, 100} {
		t.Run("", func(t *testing.T) {
			unspecified := EffectiveChance(stored, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)
			any := EffectiveChance(stored, pb.TriggerMode_TRIGGER_MODE_ANY)
			if unspecified != any {
				t.Errorf("EffectiveChance(%d, UNSPECIFIED) = %d, want %d (same as ANY)", stored, unspecified, any)
			}
		})
	}
}

func TestEffectiveChanceNeverReturnsZero(t *testing.T) {
	for _, mode := range []pb.TriggerMode{
		pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED,
		pb.TriggerMode_TRIGGER_MODE_EXACT,
		pb.TriggerMode_TRIGGER_MODE_ANY,
		pb.TriggerMode_TRIGGER_MODE_REGEX,
	} {
		for _, stored := range []int32{-100, -1, 0} {
			if got := EffectiveChance(stored, mode); got == 0 {
				t.Errorf("EffectiveChance(%d, %v) = 0, want > 0", stored, mode)
			}
		}
	}
}
