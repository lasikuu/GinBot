package command

import (
	"testing"
)

// equalArgs treats nil and an empty slice as the same result: a command with no
// arguments has nothing to distinguish, and the contract does not say which of
// the two ParseChat returns.
func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestParseChat(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		prefixes []string
		wantName string
		wantArgs []string
	}{
		{
			name:     "bare command",
			content:  "?ping",
			prefixes: []string{"?"},
			wantName: "ping",
		},
		{
			name:     "single argument",
			content:  "?say hi",
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"hi"},
		},
		{
			name:     "multi-character prefix",
			content:  "gin!ping",
			prefixes: []string{"gin!"},
			wantName: "ping",
		},
		{
			name:     "quoted argument",
			content:  `??say "hello world" now`,
			prefixes: []string{"??"},
			wantName: "say",
			wantArgs: []string{"hello world", "now"},
		},
		{
			name:     "quoted argument between plain ones",
			content:  `?say a "b c" d`,
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"a", "b c", "d"},
		},
		{
			name:     "extra whitespace after a quoted argument",
			content:  `?say "a b"    c`,
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"a b", "c"},
		},
		{
			name:     "unterminated quote takes the rest of the line",
			content:  `?say "hello world now`,
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"hello world now"},
		},
		{
			name:     "unterminated quote after a plain argument",
			content:  `?say a "b c`,
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"a", "b c"},
		},
		{
			// Quoting is the only way to pass an empty argument, so the empty
			// pair must survive as an argument rather than vanish.
			name:     "empty quoted string is an argument",
			content:  `?say ""`,
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{""},
		},
		{
			name:     "empty quoted string between arguments",
			content:  `?say a "" b`,
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"a", "", "b"},
		},
		{
			name:     "repeated internal whitespace does not produce empty arguments",
			content:  "?say a     b",
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"a", "b"},
		},
		{
			name:     "trailing whitespace is ignored",
			content:  "?ping     ",
			prefixes: []string{"?"},
			wantName: "ping",
		},
		{
			// Consistent with a prefix followed by nothing but whitespace being
			// rejected: the prefix is stripped and the remainder is split.
			name:     "whitespace between the prefix and the command name",
			content:  "?   ping",
			prefixes: []string{"?"},
			wantName: "ping",
		},
		{
			name:     "tabs separate arguments",
			content:  "?say\ta\tb",
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"a", "b"},
		},
		{
			name:     "newlines separate arguments",
			content:  "?say a\nb",
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"a", "b"},
		},
		{
			name:     "trailing newline is ignored",
			content:  "?ping\n",
			prefixes: []string{"?"},
			wantName: "ping",
		},
		{
			name:     "mixed whitespace around a quoted argument",
			content:  "?say\t\"a b\"\nc",
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"a b", "c"},
		},
		{
			name:     "longest prefix wins",
			content:  "??ping",
			prefixes: []string{"?", "??"},
			wantName: "ping",
		},
		{
			// The same expectation with the slice reversed: the order in which
			// prefixes are configured must not change the result.
			name:     "longest prefix wins regardless of slice order",
			content:  "??ping",
			prefixes: []string{"??", "?"},
			wantName: "ping",
		},
		{
			name:     "shorter prefix still matches when a longer one exists",
			content:  "?ping",
			prefixes: []string{"?", "??"},
			wantName: "ping",
		},
		{
			name:     "three prefixes of differing length",
			content:  "???ping",
			prefixes: []string{"?", "???", "??"},
			wantName: "ping",
		},
		{
			// Only one prefix is stripped. With "??" unconfigured the second "?"
			// is part of the command name, which then simply fails to resolve.
			name:     "unmatched extra prefix character stays in the name",
			content:  "??ping",
			prefixes: []string{"?"},
			wantName: "?ping",
		},
		{
			// Case folding belongs to Registry.Lookup, so the name comes back
			// exactly as it was typed.
			name:     "command name keeps its case",
			content:  "?PiNg",
			prefixes: []string{"?"},
			wantName: "PiNg",
		},
		{
			name:     "unicode command name",
			content:  "?pingü",
			prefixes: []string{"?"},
			wantName: "pingü",
		},
		{
			name:     "unicode arguments",
			content:  "?say 日本語 テスト",
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"日本語", "テスト"},
		},
		{
			name:     "quoted unicode argument containing a space",
			content:  `?say "日本 語"`,
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"日本 語"},
		},
		{
			name:     "an argument may itself look like a prefix",
			content:  "?say ?ping",
			prefixes: []string{"?"},
			wantName: "say",
			wantArgs: []string{"?ping"},
		},
		{
			// An empty entry must be ignored rather than matching everything,
			// otherwise every message becomes a command.
			name:     "empty prefix entry is ignored while a real one still works",
			content:  "?ping",
			prefixes: []string{"", "?"},
			wantName: "ping",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, args, ok := ParseChat(tt.content, tt.prefixes)
			if !ok {
				t.Fatalf("ParseChat(%q, %q) ok = false, want true", tt.content, tt.prefixes)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if !equalArgs(args, tt.wantArgs) {
				t.Errorf("args = %q, want %q", args, tt.wantArgs)
			}
		})
	}
}

func TestParseChatRejects(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		prefixes []string
	}{
		{
			name:     "message does not start with a prefix",
			content:  "ping",
			prefixes: []string{"?"},
		},
		{
			name:     "empty message",
			content:  "",
			prefixes: []string{"?"},
		},
		{
			name:     "whitespace-only message",
			content:  "   ",
			prefixes: []string{"?"},
		},
		{
			name:     "empty prefix list",
			content:  "?ping",
			prefixes: []string{},
		},
		{
			name:     "nil prefix list",
			content:  "?ping",
			prefixes: nil,
		},
		{
			// An empty prefix would otherwise turn every message into a command.
			name:     "only an empty prefix is configured",
			content:  "ping",
			prefixes: []string{""},
		},
		{
			name:     "empty prefix entry does not match arbitrary text",
			content:  "hello world",
			prefixes: []string{"", "?"},
		},
		{
			name:     "prefix with no command after it",
			content:  "?",
			prefixes: []string{"?"},
		},
		{
			name:     "longer prefix with no command after it",
			content:  "??",
			prefixes: []string{"?", "??"},
		},
		{
			name:     "prefix followed by spaces only",
			content:  "?    ",
			prefixes: []string{"?"},
		},
		{
			name:     "prefix followed by a tab and a newline only",
			content:  "?\t\n",
			prefixes: []string{"?"},
		},
		{
			// The contract is "starts with a prefix", so an indented message is
			// ordinary chat, not a command.
			name:     "leading whitespace before the prefix",
			content:  "  ?ping",
			prefixes: []string{"?"},
		},
		{
			name:     "prefix appears mid-message",
			content:  "hey ?ping",
			prefixes: []string{"?"},
		},
		{
			name:     "prefix is a suffix of the message only",
			content:  "ping?",
			prefixes: []string{"?"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, args, ok := ParseChat(tt.content, tt.prefixes)
			if ok {
				t.Fatalf("ParseChat(%q, %q) ok = true (name=%q args=%q), want false",
					tt.content, tt.prefixes, name, args)
			}
			if name != "" {
				t.Errorf("name = %q, want empty when ok is false", name)
			}
			if len(args) != 0 {
				t.Errorf("args = %q, want empty when ok is false", args)
			}
		})
	}
}

// Where case folding happens is an implementation detail of ParseChat and
// Lookup together; what must hold is that a differently cased chat command
// still reaches its handler.
func TestParseChatThenLookupIsCaseInsensitive(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Command{Name: "ping", Aliases: []string{"pong"}, Handler: noopHandler}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for _, content := range []string{"?ping", "?PING", "?Ping", "?pInG", "?pong", "?PONG", "?PoNg"} {
		t.Run(content, func(t *testing.T) {
			name, _, ok := ParseChat(content, []string{"?"})
			if !ok {
				t.Fatalf("ParseChat(%q) ok = false", content)
			}

			cmd, found := r.Lookup(name)
			if !found {
				t.Fatalf("Lookup(%q) found nothing", name)
			}
			if cmd.Name != "ping" {
				t.Errorf("resolved to %q, want %q", cmd.Name, "ping")
			}
		})
	}
}
