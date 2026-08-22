package command

import (
	"testing"
)

func TestInvocationAccessorsReadSuppliedValues(t *testing.T) {
	inv := &Invocation{Args: map[string]any{
		"s": "text",
		"n": int64(-7),
		"b": true,
	}}

	if got := inv.String("s"); got != "text" {
		t.Errorf("String(\"s\") = %q, want %q", got, "text")
	}
	if got := inv.Int("n"); got != -7 {
		t.Errorf("Int(\"n\") = %d, want -7", got)
	}
	if got := inv.Bool("b"); !got {
		t.Error("Bool(\"b\") = false, want true")
	}
	for _, name := range []string{"s", "n", "b"} {
		if !inv.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
}

// Without a declaration to fall back on there is no default, so the accessors
// must return the zero value of their own type.
func TestInvocationAccessorsOnAbsentKeys(t *testing.T) {
	tests := []struct {
		name string
		inv  *Invocation
	}{
		{"nil Args map", &Invocation{}},
		{"explicitly nil Args map", &Invocation{Args: nil}},
		{"empty Args map", &Invocation{Args: map[string]any{}}},
		{"unrelated keys only", &Invocation{Args: map[string]any{"other": "value"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.inv.String("missing"); got != "" {
				t.Errorf("String = %q, want empty", got)
			}
			if got := tt.inv.Int("missing"); got != 0 {
				t.Errorf("Int = %d, want 0", got)
			}
			if got := tt.inv.Bool("missing"); got {
				t.Error("Bool = true, want false")
			}
			if tt.inv.Has("missing") {
				t.Error("Has = true, want false")
			}
		})
	}
}

// Args is a map[string]any, so a mismatched type is reachable — from a hand-built
// Invocation in a test, or from a future caller that populates it directly.
// Reading it must degrade rather than take the process down.
func TestInvocationAccessorsIgnoreWrongGoTypes(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("accessor panicked on a mistyped value: %v", r)
		}
	}()

	inv := &Invocation{Args: map[string]any{
		"stringHoldingInt":  "12",
		"intHoldingString":  int64(12),
		"boolHoldingString": "true",
		"nilValue":          nil,
	}}

	if got := inv.Int("stringHoldingInt"); got != 0 {
		t.Errorf("Int on a string value = %d, want 0", got)
	}
	if got := inv.Bool("boolHoldingString"); got {
		t.Error("Bool on a string value = true, want false")
	}
	if got := inv.String("intHoldingString"); got != "" {
		t.Errorf("String on an int64 value = %q, want empty", got)
	}
	if got := inv.String("nilValue"); got != "" {
		t.Errorf("String on a nil value = %q, want empty", got)
	}
	if got := inv.Int("nilValue"); got != 0 {
		t.Errorf("Int on a nil value = %d, want 0", got)
	}
	if got := inv.Bool("nilValue"); got {
		t.Error("Bool on a nil value = true, want false")
	}
}

// Has answers "did the caller supply this?", which is the question a handler
// needs when a zero value and an omitted argument mean different things. A key
// present with a nil value was still supplied.
func TestInvocationHasReportsSupplyNotValue(t *testing.T) {
	inv := &Invocation{Args: map[string]any{
		"emptyString": "",
		"zeroInt":     int64(0),
		"falseBool":   false,
		"nilValue":    nil,
	}}

	for _, name := range []string{"emptyString", "zeroInt", "falseBool", "nilValue"} {
		if !inv.Has(name) {
			t.Errorf("Has(%q) = false; the key is present, so it was supplied", name)
		}
	}
	if inv.Has("absent") {
		t.Error("Has(\"absent\") = true, want false")
	}
}

// The defaults an accessor falls back on come from the command declaration, so
// they are only observable through an Invocation that Bind produced.
func TestInvocationDefaultsComeFromTheDeclaration(t *testing.T) {
	cmd := Command{
		Name: "declared",
		Args: []Arg{
			{Name: "withDefault", Type: ArgString, Default: "fallback"},
			{Name: "noDefault", Type: ArgString},
			{Name: "intWithDefault", Type: ArgInt, Default: int64(9)},
			{Name: "intNoDefault", Type: ArgInt},
			{Name: "boolWithDefault", Type: ArgBool, Default: true},
			{Name: "boolNoDefault", Type: ArgBool},
		},
		Handler: noopHandler,
	}

	inv, err := Bind(cmd, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if got := inv.String("withDefault"); got != "fallback" {
		t.Errorf("String(\"withDefault\") = %q, want %q", got, "fallback")
	}
	if got := inv.String("noDefault"); got != "" {
		t.Errorf("String(\"noDefault\") = %q, want empty", got)
	}
	if got := inv.Int("intWithDefault"); got != 9 {
		t.Errorf("Int(\"intWithDefault\") = %d, want 9", got)
	}
	if got := inv.Int("intNoDefault"); got != 0 {
		t.Errorf("Int(\"intNoDefault\") = %d, want 0", got)
	}
	if got := inv.Bool("boolWithDefault"); !got {
		t.Error("Bool(\"boolWithDefault\") = false, want the declared true")
	}
	if got := inv.Bool("boolNoDefault"); got {
		t.Error("Bool(\"boolNoDefault\") = true, want false")
	}

	// A default is not a supplied value, whatever it happens to be.
	for _, arg := range cmd.Args {
		if inv.Has(arg.Name) {
			t.Errorf("Has(%q) = true; nothing was supplied", arg.Name)
		}
	}

	// An argument that was never declared has no default to fall back on.
	if got := inv.String("undeclared"); got != "" {
		t.Errorf("String(\"undeclared\") = %q, want empty", got)
	}
}

// Reading an argument under the wrong accessor is a handler bug, not a reason to
// crash the bot: a string argument read as an integer degrades to the default.
func TestInvocationAccessorMismatchFallsBackToDefault(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("accessor panicked on a type mismatch: %v", r)
		}
	}()

	cmd := Command{
		Name:    "say",
		Args:    []Arg{{Name: "message", Type: ArgString, Required: true}},
		Handler: noopHandler,
	}

	inv, err := Bind(cmd, []string{"text"})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if got := inv.Int("message"); got != 0 {
		t.Errorf("Int on a string argument = %d, want 0", got)
	}
	if got := inv.Bool("message"); got {
		t.Error("Bool on a string argument = true, want false")
	}
}
