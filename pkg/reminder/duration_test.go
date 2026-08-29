package reminder

import (
	"testing"
	"time"
)

var (
	parseDuration = ParseDuration
)

// TestParseDurationComposite: 4M2d8h30s = 4*30d + 2d + 8h + 30s = 122d8h30s.
func TestParseDurationComposite(t *testing.T) {
	got, err := parseDuration("4M2d8h30s")
	if err != nil {
		t.Fatalf("ParseDuration(4M2d8h30s) error = %v, want nil", err)
	}

	want := 122*24*time.Hour + 8*time.Hour + 30*time.Second
	if got != want {
		t.Errorf("ParseDuration(4M2d8h30s) = %v (%d ns), want %v (%d ns)",
			got, int64(got), want, int64(want))
	}
	if int64(want) != 10569630000000000 {
		t.Fatalf("test arithmetic drifted: want ns = %d, expected 10569630000000000", int64(want))
	}
}

func TestParseDurationSingleUnits(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Duration
	}{
		{"seconds", "30s", 30 * time.Second},
		{"minutes", "5m", 5 * time.Minute},
		{"hours", "2h", 2 * time.Hour},
		{"days", "3d", 3 * 24 * time.Hour},
		{"weeks", "1w", 7 * 24 * time.Hour},
		{"month", "1M", 30 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if err != nil {
				t.Fatalf("ParseDuration(%q) error = %v, want nil", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseDurationCaseSensitiveMonthVsMinute(t *testing.T) {
	month, err := parseDuration("1M")
	if err != nil {
		t.Fatalf("ParseDuration(1M) error = %v", err)
	}
	minute, err := parseDuration("1m")
	if err != nil {
		t.Fatalf("ParseDuration(1m) error = %v", err)
	}
	if month == minute {
		t.Fatalf("1M and 1m parsed to the same duration %v; case sensitivity lost", month)
	}
	if month != 30*24*time.Hour {
		t.Errorf("1M = %v, want 720h (30 days)", month)
	}
	if minute != time.Minute {
		t.Errorf("1m = %v, want 1m", minute)
	}
}

func TestParseDurationRejects(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"zero total", "0s"},
		{"negative", "-5m"},
		{"missing number", "h"},
		{"unknown unit", "5y"},
		{"bare number no unit", "5"},
		{"garbage", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if err == nil {
				t.Errorf("ParseDuration(%q) = %v, want error", tt.input, got)
			}
		})
	}
}

func TestParseDurationRepeatedUnit(t *testing.T) {
	got, err := parseDuration("5m5m")
	if err == nil {
		t.Errorf("ParseDuration(5m5m) = %v, want error (repeated unit rejected)", got)
	}
}

// TestParseDurationOverflow: time.Duration is int64 ns, so it maxes at ~292
// years and must error rather than wrap.
func TestParseDurationOverflow(t *testing.T) {
	got, err := parseDuration("4000000000h")
	if err == nil {
		t.Errorf("ParseDuration(4000000000h) = %v, want overflow error", got)
	}
	if got < 0 {
		t.Errorf("ParseDuration overflowed to a negative duration %v; it wrapped instead of erroring", got)
	}
}
