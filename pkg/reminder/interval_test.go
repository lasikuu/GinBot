package reminder

import (
	"testing"
)

var (
	validateRepeatInterval = ValidateRepeatInterval

	channelFloor = MinIntervalPublic
	dmFloor      = MinIntervalDM
)

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

// TestValidateRepeatIntervalDMLooserThanChannel pins that the two floors are
// genuinely different values.
func TestValidateRepeatIntervalDMLooserThanChannel(t *testing.T) {
	const expr = "@every 15m"

	if err := validateRepeatInterval(expr, false); err == nil {
		t.Errorf("ValidateRepeatInterval(%q, channel) = nil, want error", expr)
	}
	if err := validateRepeatInterval(expr, true); err != nil {
		t.Errorf("ValidateRepeatInterval(%q, dm) = %v, want nil", expr, err)
	}
}
