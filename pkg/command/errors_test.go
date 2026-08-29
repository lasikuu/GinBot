package command

import (
	"testing"

	"connectrpc.com/connect"
)

// TestBindErrorsCarryConnectCodes pins the error taxonomy; every other test in
// this package only checks err != nil, which any error type satisfies.
func TestBindErrorsCarryConnectCodes(t *testing.T) {
	t.Run("missing required argument", func(t *testing.T) {
		cmd := Command{
			Name:    "say",
			Args:    []Arg{{Name: "message", Type: ArgString, Required: true}},
			Handler: noopHandler,
		}

		_, err := BindNamed(cmd, map[string]any{})
		if err == nil {
			t.Fatal("BindNamed succeeded, want an error for the missing required argument")
		}
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
		}
	})

	t.Run("unparsable integer from a chat token", func(t *testing.T) {
		_, err := Bind(intCmd(), []string{"not a number"})
		if err == nil {
			t.Fatal("Bind succeeded, want an error for an unparsable integer")
		}
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
		}
	})

	t.Run("unparsable bool from a chat token", func(t *testing.T) {
		_, err := Bind(boolCmd(), []string{"not a bool"})
		if err == nil {
			t.Fatal("Bind succeeded, want an error for an unparsable bool")
		}
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
		}
	})

	t.Run("wrong Go type for a named argument", func(t *testing.T) {
		_, err := BindNamed(intCmd(), map[string]any{"n": "not an int64"})
		if err == nil {
			t.Fatal("BindNamed succeeded, want an error for the wrong Go type")
		}
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
		}
	})

	// pkg/discord's errorMessage relies on Internal to avoid showing an
	// internal defect to the caller as if they had mistyped.
	t.Run("unknown ArgType is internal, not invalid argument", func(t *testing.T) {
		cmd := Command{
			Name:    "broken",
			Args:    []Arg{{Name: "x", Type: ArgType(99)}},
			Handler: noopHandler,
		}

		_, err := Bind(cmd, []string{"value"})
		if err == nil {
			t.Fatal("Bind succeeded, want an error for the unknown argument type")
		}
		if got := connect.CodeOf(err); got != connect.CodeInternal {
			t.Errorf("code = %v, want %v (a bad command declaration is not the caller's mistake)", got, connect.CodeInternal)
		}

		_, err = BindNamed(cmd, map[string]any{"x": "value"})
		if err == nil {
			t.Fatal("BindNamed succeeded, want an error for the unknown argument type")
		}
		if got := connect.CodeOf(err); got != connect.CodeInternal {
			t.Errorf("code = %v, want %v", got, connect.CodeInternal)
		}
	})
}
