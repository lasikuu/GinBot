package command

import (
	"math"
	"strconv"
	"testing"
)

func intCmd() Command {
	return Command{
		Name:    "number",
		Args:    []Arg{{Name: "n", Description: "A number", Type: ArgInt, Required: true}},
		Handler: noopHandler,
	}
}

func boolCmd() Command {
	return Command{
		Name:    "toggle",
		Args:    []Arg{{Name: "b", Description: "A flag", Type: ArgBool, Required: true}},
		Handler: noopHandler,
	}
}

func TestBindRejectsMissingRequiredArgument(t *testing.T) {
	cmd := Command{
		Name: "say",
		Args: []Arg{
			{Name: "first", Type: ArgString, Required: true},
			{Name: "second", Type: ArgString, Required: true},
		},
		Handler: noopHandler,
	}

	tests := []struct {
		name string
		raw  []string
	}{
		{"nil arguments", nil},
		{"no arguments", []string{}},
		{"only the first argument", []string{"a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Bind(cmd, tt.raw); err == nil {
				t.Fatalf("Bind(%q) succeeded, want an error for the missing required argument", tt.raw)
			}
		})
	}
}

func TestBindDefaultDoesNotSatisfyRequired(t *testing.T) {
	cmd := Command{
		Name:    "say",
		Args:    []Arg{{Name: "message", Type: ArgString, Required: true, Default: "fallback"}},
		Handler: noopHandler,
	}

	if _, err := Bind(cmd, nil); err == nil {
		t.Fatal("Bind succeeded; a Default must not satisfy a required argument")
	}
}

func TestBindIntArguments(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{name: "plain", raw: "5", want: 5},
		{name: "zero", raw: "0", want: 0},
		{name: "negative", raw: "-5", want: -5},
		{name: "explicit plus sign", raw: "+5", want: 5},
		{name: "leading zeroes", raw: "007", want: 7},
		{name: "int64 max", raw: strconv.FormatInt(math.MaxInt64, 10), want: math.MaxInt64},
		{name: "int64 min", raw: strconv.FormatInt(math.MinInt64, 10), want: math.MinInt64},

		{name: "overflows int64", raw: "9223372036854775808", wantErr: true},
		{name: "underflows int64", raw: "-9223372036854775809", wantErr: true},
		{name: "far beyond int64", raw: "99999999999999999999999999", wantErr: true},
		{name: "float", raw: "1.5", wantErr: true},
		{name: "float with a zero fraction", raw: "1.0", wantErr: true},
		{name: "scientific notation", raw: "1e3", wantErr: true},
		{name: "not a number", raw: "abc", wantErr: true},
		{name: "empty string", raw: "", wantErr: true},
		{name: "leading space", raw: " 5", wantErr: true},
		{name: "trailing space", raw: "5 ", wantErr: true},
		{name: "hexadecimal", raw: "0x10", wantErr: true},
		{name: "digit separators", raw: "1_000", wantErr: true},
		{name: "number with a suffix", raw: "5s", wantErr: true},
		{name: "non-ascii digit", raw: "٥", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := Bind(intCmd(), []string{tt.raw})

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Bind(%q) succeeded, want a type error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("Bind(%q): %v", tt.raw, err)
			}
			if got := inv.Int("n"); got != tt.want {
				t.Errorf("Int(\"n\") = %d, want %d", got, tt.want)
			}
			if !inv.Has("n") {
				t.Error("Has(\"n\") = false, want true for a supplied argument")
			}
		})
	}
}

func TestBindBoolArguments(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{name: "true", raw: "true", want: true},
		{name: "True", raw: "True", want: true},
		{name: "TRUE", raw: "TRUE", want: true},
		{name: "t", raw: "t", want: true},
		{name: "T", raw: "T", want: true},
		{name: "one", raw: "1", want: true},
		{name: "false", raw: "false", want: false},
		{name: "False", raw: "False", want: false},
		{name: "FALSE", raw: "FALSE", want: false},
		{name: "f", raw: "f", want: false},
		{name: "F", raw: "F", want: false},
		{name: "zero", raw: "0", want: false},

		{name: "yes", raw: "yes", wantErr: true},
		{name: "no", raw: "no", wantErr: true},
		{name: "on", raw: "on", wantErr: true},
		{name: "off", raw: "off", wantErr: true},
		{name: "mixed case true", raw: "TrUe", wantErr: true},
		{name: "empty string", raw: "", wantErr: true},
		{name: "two", raw: "2", wantErr: true},
		{name: "garbage", raw: "maybe", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := Bind(boolCmd(), []string{tt.raw})

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Bind(%q) succeeded, want a type error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("Bind(%q): %v", tt.raw, err)
			}
			if got := inv.Bool("b"); got != tt.want {
				t.Errorf("Bool(\"b\") = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBindAppliesDefaults(t *testing.T) {
	cmd := Command{
		Name: "roll",
		Args: []Arg{
			{Name: "s", Type: ArgString, Default: "fallback"},
			{Name: "n", Type: ArgInt, Default: int64(42)},
			{Name: "b", Type: ArgBool, Default: true},
		},
		Handler: noopHandler,
	}

	t.Run("all absent", func(t *testing.T) {
		inv, err := Bind(cmd, nil)
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if got := inv.String("s"); got != "fallback" {
			t.Errorf("String(\"s\") = %q, want %q", got, "fallback")
		}
		if got := inv.Int("n"); got != 42 {
			t.Errorf("Int(\"n\") = %d, want 42", got)
		}
		if got := inv.Bool("b"); !got {
			t.Error("Bool(\"b\") = false, want the declared default true")
		}
		for _, name := range []string{"s", "n", "b"} {
			if inv.Has(name) {
				t.Errorf("Has(%q) = true; a default is not a supplied argument", name)
			}
		}
	})

	t.Run("first supplied, rest defaulted", func(t *testing.T) {
		inv, err := Bind(cmd, []string{"given"})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if got := inv.String("s"); got != "given" {
			t.Errorf("String(\"s\") = %q, want %q", got, "given")
		}
		if !inv.Has("s") {
			t.Error("Has(\"s\") = false, want true")
		}
		if got := inv.Int("n"); got != 42 {
			t.Errorf("Int(\"n\") = %d, want the default 42", got)
		}
		if inv.Has("n") {
			t.Error("Has(\"n\") = true, want false for an absent argument")
		}
	})

	t.Run("a supplied value beats the default", func(t *testing.T) {
		inv, err := Bind(cmd, []string{"given", "7", "false"})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if got := inv.Int("n"); got != 7 {
			t.Errorf("Int(\"n\") = %d, want 7", got)
		}
		if got := inv.Bool("b"); got {
			t.Error("Bool(\"b\") = true, want the supplied false")
		}
	})
}

func TestBindDefaultOfWrongGoType(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on a mistyped Default: %v", r)
		}
	}()

	cmd := Command{
		Name: "roll",
		Args: []Arg{
			{Name: "n", Type: ArgInt, Default: "not an int"},
			{Name: "s", Type: ArgString, Default: int64(3)},
			{Name: "b", Type: ArgBool, Default: "true"},
		},
		Handler: noopHandler,
	}

	inv, err := Bind(cmd, nil)
	if err != nil {
		// Rejecting the mistyped declaration outright is acceptable.
		t.Logf("Bind rejected the mistyped defaults: %v", err)
		return
	}

	if got := inv.Int("n"); got != 0 {
		t.Errorf("Int(\"n\") = %d, want 0 for a string Default", got)
	}
	if got := inv.String("s"); got != "" {
		t.Errorf("String(\"s\") = %q, want empty for an int64 Default", got)
	}
	if got := inv.Bool("b"); got {
		t.Error("Bool(\"b\") = true, want false for a string Default")
	}
}

func TestBindDefaultDeclaredAsPlainInt(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on an int (rather than int64) Default: %v", r)
		}
	}()

	cmd := Command{
		Name:    "roll",
		Args:    []Arg{{Name: "n", Type: ArgInt, Default: 5}},
		Handler: noopHandler,
	}

	inv, err := Bind(cmd, nil)
	if err != nil {
		t.Fatalf("Bind rejected an int Default: %v", err)
	}

	if got := inv.Int("n"); got != 5 {
		t.Errorf("Int(\"n\") = %d, want 5", got)
	}
}

func TestBindIgnoresExtraPositionalArguments(t *testing.T) {
	cmd := Command{
		Name:    "say",
		Args:    []Arg{{Name: "only", Type: ArgString, Required: true}},
		Handler: noopHandler,
	}

	inv, err := Bind(cmd, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got := inv.String("only"); got != "a" {
		t.Errorf("String(\"only\") = %q, want %q", got, "a")
	}
	if inv.Has("b") || inv.Has("c") {
		t.Error("surplus arguments were stored under their own values as names")
	}
	if len(inv.Args) != 1 {
		t.Errorf("Args = %v, want only the declared argument", inv.Args)
	}
}

func TestBindWithoutDeclaredArgs(t *testing.T) {
	cmd := Command{Name: "ping", Handler: noopHandler}

	tests := []struct {
		name string
		raw  []string
	}{
		{"nil arguments", nil},
		{"empty arguments", []string{}},
		{"surplus arguments", []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := Bind(cmd, tt.raw)
			if err != nil {
				t.Fatalf("Bind(%q): %v", tt.raw, err)
			}
			if inv == nil {
				t.Fatal("Bind returned a nil Invocation without an error")
			}
			if len(inv.Args) != 0 {
				t.Errorf("Args = %v, want empty for a command with no declared arguments", inv.Args)
			}
			if inv.String("anything") != "" || inv.Int("anything") != 0 || inv.Bool("anything") {
				t.Error("accessors returned a value for an undeclared argument")
			}
		})
	}
}

func TestBindRequiredAfterOptional(t *testing.T) {
	cmd := Command{
		Name: "awkward",
		Args: []Arg{
			{Name: "optional", Type: ArgString},
			{Name: "required", Type: ArgString, Required: true},
		},
		Handler: noopHandler,
	}

	if _, err := Bind(cmd, []string{"x"}); err == nil {
		t.Error("Bind succeeded with one value; the required argument is still missing")
	}

	inv, err := Bind(cmd, []string{"x", "y"})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got := inv.String("optional"); got != "x" {
		t.Errorf("String(\"optional\") = %q, want %q", got, "x")
	}
	if got := inv.String("required"); got != "y" {
		t.Errorf("String(\"required\") = %q, want %q", got, "y")
	}
}

func TestBindStringKeepsSpaces(t *testing.T) {
	cmd := Command{
		Name:    "say",
		Args:    []Arg{{Name: "message", Type: ArgString, Required: true}},
		Handler: noopHandler,
	}

	inv, err := Bind(cmd, []string{"hello world  again"})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got := inv.String("message"); got != "hello world  again" {
		t.Errorf("String(\"message\") = %q, want the value unchanged", got)
	}
}

func TestBindEmptyStringSatisfiesRequired(t *testing.T) {
	cmd := Command{
		Name:    "say",
		Args:    []Arg{{Name: "message", Type: ArgString, Required: true, Default: "fallback"}},
		Handler: noopHandler,
	}

	inv, err := Bind(cmd, []string{""})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got := inv.String("message"); got != "" {
		t.Errorf("String(\"message\") = %q, want the supplied empty string", got)
	}
	if !inv.Has("message") {
		t.Error("Has(\"message\") = false, want true for a supplied empty string")
	}
}

func TestBindMixedTypesInOrder(t *testing.T) {
	cmd := Command{
		Name: "mixed",
		Args: []Arg{
			{Name: "s", Type: ArgString, Required: true},
			{Name: "n", Type: ArgInt, Required: true},
			{Name: "b", Type: ArgBool, Required: true},
		},
		Handler: noopHandler,
	}

	inv, err := Bind(cmd, []string{"text", "-12", "true"})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got := inv.String("s"); got != "text" {
		t.Errorf("String(\"s\") = %q, want %q", got, "text")
	}
	if got := inv.Int("n"); got != -12 {
		t.Errorf("Int(\"n\") = %d, want -12", got)
	}
	if got := inv.Bool("b"); !got {
		t.Error("Bool(\"b\") = false, want true")
	}
}

func TestBindNamedMatchesBind(t *testing.T) {
	cmd := Command{
		Name: "doubles",
		Args: []Arg{
			{Name: "digits", Type: ArgInt, Default: int64(2)},
			{Name: "label", Type: ArgString, Default: "roll"},
			{Name: "loud", Type: ArgBool, Default: false},
		},
		Handler: noopHandler,
	}

	tests := []struct {
		name  string
		raw   []string
		named map[string]any
	}{
		{
			name:  "nothing supplied",
			raw:   nil,
			named: map[string]any{},
		},
		{
			name:  "first argument supplied",
			raw:   []string{"4"},
			named: map[string]any{"digits": int64(4)},
		},
		{
			name:  "all arguments supplied",
			raw:   []string{"6", "sexts", "true"},
			named: map[string]any{"digits": int64(6), "label": "sexts", "loud": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fromChat, err := Bind(cmd, tt.raw)
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}
			fromSlash, err := BindNamed(cmd, tt.named)
			if err != nil {
				t.Fatalf("BindNamed: %v", err)
			}

			for _, arg := range cmd.Args {
				if fromChat.Has(arg.Name) != fromSlash.Has(arg.Name) {
					t.Errorf("Has(%q) = %v via chat, %v via slash",
						arg.Name, fromChat.Has(arg.Name), fromSlash.Has(arg.Name))
				}
			}
			if fromChat.Int("digits") != fromSlash.Int("digits") {
				t.Errorf("digits = %d via chat, %d via slash", fromChat.Int("digits"), fromSlash.Int("digits"))
			}
			if fromChat.String("label") != fromSlash.String("label") {
				t.Errorf("label = %q via chat, %q via slash", fromChat.String("label"), fromSlash.String("label"))
			}
			if fromChat.Bool("loud") != fromSlash.Bool("loud") {
				t.Errorf("loud = %v via chat, %v via slash", fromChat.Bool("loud"), fromSlash.Bool("loud"))
			}
		})
	}
}

func TestBindNamedRejectsMismatchedTypes(t *testing.T) {
	cmd := Command{
		Name: "mixed",
		Args: []Arg{
			{Name: "s", Type: ArgString},
			{Name: "n", Type: ArgInt},
			{Name: "b", Type: ArgBool},
		},
		Handler: noopHandler,
	}

	tests := []struct {
		name  string
		named map[string]any
	}{
		{"string where an int is declared", map[string]any{"n": "12"}},
		{"bool where an int is declared", map[string]any{"n": true}},
		{"int where a string is declared", map[string]any{"s": int64(12)}},
		{"string where a bool is declared", map[string]any{"b": "true"}},
		{"nil where a string is declared", map[string]any{"s": nil}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BindNamed(cmd, tt.named); err == nil {
				t.Fatalf("BindNamed(%v) succeeded, want a type error", tt.named)
			}
		})
	}
}

func TestBindNamedRequiredAndUnknown(t *testing.T) {
	cmd := Command{
		Name:    "say",
		Args:    []Arg{{Name: "message", Type: ArgString, Required: true}},
		Handler: noopHandler,
	}

	if _, err := BindNamed(cmd, map[string]any{}); err == nil {
		t.Error("BindNamed succeeded without the required argument")
	}
	if _, err := BindNamed(cmd, nil); err == nil {
		t.Error("BindNamed succeeded with a nil map and a required argument")
	}

	inv, err := BindNamed(cmd, map[string]any{"message": "hi", "surplus": int64(1)})
	if err != nil {
		t.Fatalf("BindNamed: %v", err)
	}
	if got := inv.String("message"); got != "hi" {
		t.Errorf("String(\"message\") = %q, want %q", got, "hi")
	}
	if inv.Has("surplus") {
		t.Error("Has(\"surplus\") = true; an undeclared option must not be bound")
	}
}

func TestBindRejectsUnknownArgType(t *testing.T) {
	cmd := Command{
		Name:    "broken",
		Args:    []Arg{{Name: "x", Type: ArgType(99)}},
		Handler: noopHandler,
	}

	if _, err := Bind(cmd, []string{"value"}); err == nil {
		t.Error("Bind accepted an argument with an unknown type")
	}
	if _, err := BindNamed(cmd, map[string]any{"x": "value"}); err == nil {
		t.Error("BindNamed accepted an argument with an unknown type")
	}
}

func TestBindReportsLaterTypeErrors(t *testing.T) {
	cmd := Command{
		Name: "mixed",
		Args: []Arg{
			{Name: "s", Type: ArgString, Required: true},
			{Name: "n", Type: ArgInt},
		},
		Handler: noopHandler,
	}

	if _, err := Bind(cmd, []string{"text", "not a number"}); err == nil {
		t.Fatal("Bind succeeded with an unparsable optional integer")
	}
}
