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

// TestDiscordPassthroughAccessorsAreVerbatim: three of the Discord accessors
// do nothing but read the variable. Pinned so that a later "helpful" default
// — a placeholder owner id, say — cannot be introduced silently, and because
// an empty value here means "unset" to every caller downstream.
func TestDiscordPassthroughAccessorsAreVerbatim(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		read   func() string
		sample string
	}{
		{"owner id", "DISCORD_OWNER_ID", ownerId, "123456789012345678"},
		{"client id", "DISCORD_CLIENT_ID", clientId, "987654321098765432"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetEnv(t, tt.env)
			if got := tt.read(); got != "" {
				t.Errorf("%s unset = %q, want the empty string", tt.env, got)
			}

			t.Setenv(tt.env, tt.sample)
			if got := tt.read(); got != tt.sample {
				t.Errorf("%s = %q, want %q", tt.env, got, tt.sample)
			}
		})
	}
}

// TestBotTokenAppliesTheBotPrefixExactlyOnce is the accessor worth the most
// attention in this package: it is the only one that TRANSFORMS its value, and
// AGENTS.md calls it out by name because operators keep writing "Bot xyz" into
// .env themselves. Discord rejects "Bot Bot xyz" with a 401 at gateway
// connect, which surfaces nowhere near the configuration that caused it.
//
// The unset case is pinned deliberately too: it yields the bare prefix "Bot ",
// NOT the empty string, so a caller cannot test the token for emptiness to
// decide whether Discord is configured.
func TestBotTokenAppliesTheBotPrefixExactlyOnce(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  string
	}{
		{"unset still yields the bare prefix", false, "", "Bot "},
		{"an empty value still yields the bare prefix", true, "", "Bot "},
		{"a raw token is prefixed", true, "abc.def.ghi", "Bot abc.def.ghi"},
		{"an already-prefixed token is prefixed AGAIN, not deduplicated", true, "Bot abc", "Bot Bot abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("DISCORD_BOT_TOKEN", tt.value)
			} else {
				unsetEnv(t, "DISCORD_BOT_TOKEN")
			}

			if got := botToken(); got != tt.want {
				t.Errorf("botToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDiscordBooleanGatesRequireTheExactLiteralTrue: both are `== "true"`, so
// every other spelling leaves the feature OFF. That is the safe direction for
// DISCORD_REMOVE_COMMANDS, which deletes every registered slash command, and
// the load-bearing one for DISCORD_MESSAGE_CONTENT, which opts into a
// privileged intent — Discord closes the gateway with 4014 if the bot requests
// it without approval, so "MESSAGE_CONTENT=True quietly did nothing" is a much
// better failure than "the bot will not start".
func TestDiscordBooleanGatesRequireTheExactLiteralTrue(t *testing.T) {
	gates := []struct {
		name string
		env  string
		read func() bool
	}{
		{"eraseCommands", "DISCORD_REMOVE_COMMANDS", eraseCommands},
		{"messageContent", "DISCORD_MESSAGE_CONTENT", messageContent},
	}

	values := []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{"unset", false, "", false},
		{"empty", true, "", false},
		{"the exact literal true enables it", true, "true", true},
		{"capitalised True does NOT enable it", true, "True", false},
		{"uppercase TRUE does NOT enable it", true, "TRUE", false},
		{"one does NOT enable it", true, "1", false},
		{"yes does NOT enable it", true, "yes", false},
		{"a trailing space does NOT enable it", true, "true ", false},
		{"false disables it", true, "false", false},
	}

	for _, gate := range gates {
		t.Run(gate.name, func(t *testing.T) {
			for _, v := range values {
				t.Run(v.name, func(t *testing.T) {
					if v.set {
						t.Setenv(gate.env, v.value)
					} else {
						unsetEnv(t, gate.env)
					}

					if got := gate.read(); got != v.want {
						t.Errorf("%s with %s=%q = %v, want %v", gate.name, gate.env, v.value, got, v.want)
					}
				})
			}
		})
	}
}
