package config

import (
	"os"
	"slices"
	"testing"

	"github.com/lasikuu/GinBot/pkg/command"
)

const envCommandPrefixes = "DISCORD_COMMAND_PREFIXES"

// setCommandPrefixesEnv sets the variable, or genuinely removes it when set is
// false. t.Setenv has no unset counterpart, but it does snapshot the previous
// value and restore it during cleanup, which keeps the test order-independent.
func setCommandPrefixesEnv(t *testing.T, value string, set bool) {
	t.Helper()
	t.Setenv(envCommandPrefixes, value)
	if !set {
		if err := os.Unsetenv(envCommandPrefixes); err != nil {
			t.Fatalf("unset %s: %v", envCommandPrefixes, err)
		}
	}
}

// accepts reports whether the configured prefixes make content a chat command,
// asked of the tokeniser that actually dispatches rather than of a second,
// subtly different oracle. This is what makes the table below a regression test
// for the real defect: the old regex rejected "??ping" outright.
func accepts(p CommandPrefixes, content string) bool {
	_, _, ok := command.ParseChat(content, p.Prefixes)

	return ok
}

// samePrefixSet compares as a set: nothing depends on the order in which the
// prefixes are stored, because matching is longest-first regardless.
func samePrefixSet(got, want []string) bool {
	gotSorted := slices.Clone(got)
	wantSorted := slices.Clone(want)
	slices.Sort(gotSorted)
	slices.Sort(wantSorted)
	return slices.Equal(gotSorted, wantSorted)
}

// The original implementation applied regexp.QuoteMeta to the already-joined
// string, so the "|" separating the alternatives was escaped into a literal:
// "??,!" compiled to `^(\?\?\|!).+$`, which rejected "??ping" and accepted the
// literal "??|!ping". With the variable unset it compiled to `^().+$`, which
// matched every message in every channel.
func TestCommandPrefixes(t *testing.T) {
	tests := []struct {
		name         string
		value        string
		set          bool
		wantPrefixes []string
		// accepted maps a chat message to whether it must dispatch as a command.
		accepted map[string]bool
	}{
		{
			name:         "unset disables chat commands entirely",
			set:          false,
			wantPrefixes: nil,
			accepted: map[string]bool{
				"ping":              false,
				"?ping":             false,
				"??ping":            false,
				"just talking here": false,
				"":                  false,
			},
		},
		{
			name:         "empty value disables chat commands entirely",
			value:        "",
			set:          true,
			wantPrefixes: nil,
			accepted: map[string]bool{
				"ping":   false,
				"?ping":  false,
				"??ping": false,
				"":       false,
			},
		},
		{
			name:         "single prefix",
			value:        "?",
			set:          true,
			wantPrefixes: []string{"?"},
			accepted: map[string]bool{
				"?ping":   true,
				"?p":      true,
				"ping":    false,
				"?":       false,
				"x?ping":  false,
				" ?ping":  false,
				"!ping":   false,
				"??ping":  true,
				"?? ping": true,
			},
		},
		{
			name:         "multiple prefixes",
			value:        "??,!",
			set:          true,
			wantPrefixes: []string{"??", "!"},
			accepted: map[string]bool{
				"??ping": true,
				"!ping":  true,
				"ping":   false,
				"?ping":  false,
				"??":     false,
				"!":      false,
			},
		},
		{
			name:         "three prefixes",
			value:        "?,??,gin!",
			set:          true,
			wantPrefixes: []string{"?", "??", "gin!"},
			accepted: map[string]bool{
				"?ping":    true,
				"??ping":   true,
				"gin!ping": true,
				"gin!":     false,
				"ping":     false,
			},
		},
		{
			// QuoteMeta has to be applied per element. An unescaped "." would
			// make every single character a valid prefix.
			name:         "regex metacharacters stay literal",
			value:        ".,$,+",
			set:          true,
			wantPrefixes: []string{".", "$", "+"},
			accepted: map[string]bool{
				".ping": true,
				"$ping": true,
				"+ping": true,
				"Xping": false,
				"ping":  false,
			},
		},
		{
			name:         "empty elements are dropped",
			value:        "?,,!,",
			set:          true,
			wantPrefixes: []string{"?", "!"},
			accepted: map[string]bool{
				"?ping": true,
				"!ping": true,
				"ping":  false,
			},
		},
		{
			// "?, !" is the natural way to write a list, so the surrounding
			// whitespace must not become part of the prefix.
			name:         "whitespace around a prefix is trimmed",
			value:        " ? , ! ",
			set:          true,
			wantPrefixes: []string{"?", "!"},
			accepted: map[string]bool{
				"?ping":  true,
				"!ping":  true,
				" ?ping": false,
				"ping":   false,
			},
		},
		{
			name:         "whitespace-only elements are dropped",
			value:        "  ,\t,",
			set:          true,
			wantPrefixes: nil,
			accepted: map[string]bool{
				" ping": false,
				"ping":  false,
				"?ping": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setCommandPrefixesEnv(t, tt.value, tt.set)

			got := commandPrefixes()

			if !samePrefixSet(got.Prefixes, tt.wantPrefixes) {
				t.Errorf("Prefixes = %q, want %q", got.Prefixes, tt.wantPrefixes)
			}

			for content, want := range tt.accepted {
				if accepts(got, content) != want {
					t.Errorf("dispatches(%q) = %v, want %v (prefixes %q)", content, !want, want, got.Prefixes)
				}
			}
		})
	}
}
