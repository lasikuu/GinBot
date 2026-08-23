package config

import "testing"

// Tests for internal/config/matrix.go. TestMain and unsetEnv come from
// repost_test.go.

// matrixCase names one (accessor, env var) pair so the table cannot stop
// checking that an accessor reads the variable it is documented to. There is
// no default column: all three are raw passthroughs.
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

// TestMatrixAccessorsAreEmptyWhenUnset: unlike the DB accessors these have no
// default, so an unconfigured Matrix client gets empty strings and fails at
// connect time rather than silently connecting somewhere else.
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

// TestMatrixAccessorsPassTheValueThroughVerbatim: no trimming, no
// normalisation. An access token with surrounding whitespace has to reach the
// Matrix client exactly as configured, because silently trimming it would
// make a genuinely whitespace-bearing secret unusable with no diagnostic.
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
