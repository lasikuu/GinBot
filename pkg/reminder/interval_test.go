package reminder

import (
	"testing"
)

// ── Assumed symbols from pkg/reminder ────────────────────────────────────────
//
// The minimum-interval rule.
//
//	func ValidateRepeatInterval(expr string, isDM bool) error
//
// Floors come straight from the implementation constants MinIntervalPublic (12h)
// and MinIntervalDM (10m).
//
// Boundary policy: the implementation rejects only gap < floor, so exactly at
// the floor is ALLOWED. The cases marked BOUNDARY below assert that.
var (
	validateRepeatInterval = ValidateRepeatInterval

	channelFloor = MinIntervalPublic
	dmFloor      = MinIntervalDM
)

// TestValidateRepeatIntervalChannel: a channel reminder firing more often than
// every 12h is rejected; at or slower than the floor is allowed.
func TestValidateRepeatIntervalChannel(t *testing.T) {
	const isDM = false

	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every 5m rejected", "@every 5m", true},
		{"every 15m rejected", "@every 15m", true},
		{"hourly rejected", "0 * * * *", true},
		{"every 11h rejected", "@every 11h", true},
		{"every 12h allowed (BOUNDARY)", "@every 12h", false},
		{"daily allowed", "0 9 * * *", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRepeatInterval(tt.expr, isDM)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateRepeatInterval(%q, channel) = nil, want error (floor %s)", tt.expr, channelFloor)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateRepeatInterval(%q, channel) = %v, want nil", tt.expr, err)
			}
		})
	}
}

// TestValidateRepeatIntervalDM: a DM reminder is allowed down to the 10-minute
// floor but no faster.
func TestValidateRepeatIntervalDM(t *testing.T) {
	const isDM = true

	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every 5m rejected even for DM", "@every 5m", true},
		{"every 11m allowed", "@every 11m", false},
		{"every 15m allowed", "@every 15m", false},
		{"every 10m allowed (BOUNDARY)", "@every 10m", false},
		{"daily allowed", "0 9 * * *", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRepeatInterval(tt.expr, isDM)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateRepeatInterval(%q, dm) = nil, want error (floor %s)", tt.expr, dmFloor)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateRepeatInterval(%q, dm) = %v, want nil", tt.expr, err)
			}
		})
	}
}

// TestValidateRepeatIntervalDMLooserThanChannel: the same fast cron that a
// channel refuses, a DM permits, as long as it is at or above the DM floor.
// @every 15m is the discriminating case: rejected for a channel, allowed for a
// DM. This asserts the two floors are genuinely different, not the same value.
func TestValidateRepeatIntervalDMLooserThanChannel(t *testing.T) {
	const expr = "@every 15m"

	if err := validateRepeatInterval(expr, false); err == nil {
		t.Errorf("ValidateRepeatInterval(%q, channel) = nil, want error", expr)
	}
	if err := validateRepeatInterval(expr, true); err != nil {
		t.Errorf("ValidateRepeatInterval(%q, dm) = %v, want nil", expr, err)
	}
}
