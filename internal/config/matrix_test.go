package config

import "testing"

// All three are raw passthroughs, so there is no default column.
type matrixCase struct {
	name     string
	accessor func() string
	envVar   string
}

func matrixCases() []matrixCase {
	return []matrixCase{
		{"homeServerUrl", homeServerUrl, "MATRIX_HOMESERVER_URL"},
		{"accessToken", accessToken, "MATRIX_ACCESS_TOKEN"},
		{"userId", userId, "MATRIX_USER_ID"},
	}
}

// No defaults, so an unconfigured client fails at connect time.
func TestMatrixAccessorsAreEmptyWhenUnset(t *testing.T) {
	for _, tc := range matrixCases() {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, tc.envVar)

			if got := tc.accessor(); got != "" {
				t.Errorf("%s() = %q, want empty when unset", tc.name, got)
			}
		})
	}
}

// No trimming: a genuinely whitespace-bearing secret must stay usable.
func TestMatrixAccessorsPassTheValueThroughVerbatim(t *testing.T) {
	const raw = "  https://matrix.example/  "

	for _, tc := range matrixCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envVar, raw)

			if got := tc.accessor(); got != raw {
				t.Errorf("%s() = %q, want the raw value %q", tc.name, got, raw)
			}
		})
	}
}
