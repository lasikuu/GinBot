package discord

import (
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
)

// newTestRegistry builds the real command set. The handlers are never invoked
// here — every assertion below is about declaration, not behaviour — so no gRPC
// client or Discord session is needed.
func newTestRegistry(t *testing.T) *command.Registry {
	t.Helper()

	registry := command.NewRegistry()
	for _, cmd := range commandDefinitions() {
		if err := registry.Register(cmd); err != nil {
			t.Fatalf("register %q: %v", cmd.Name, err)
		}
	}

	return registry
}

// TestCommandDefinitionsRegister is the check initCommands performs at startup,
// where the only report is log.Z.Fatal. A collision, a missing handler or an
// unbindable argument order would take the bot down on boot; here it is a test
// failure instead.
func TestCommandDefinitionsRegister(t *testing.T) {
	registry := newTestRegistry(t)

	want := []string{"doubles", "healthcheck", "number", "quads", "quints", "sexts", "triples"}

	got := make([]string, 0, len(want))
	for _, cmd := range registry.All() {
		got = append(got, cmd.Name)
	}

	if !slices.Equal(got, want) {
		t.Errorf("registered commands = %q, want %q", got, want)
	}
}

// TestDigitRollDigits pins the digit counts. The server zero-pads to this
// width, so a wrong number here silently changes what a roll looks like rather
// than failing.
func TestDigitRollDigits(t *testing.T) {
	want := map[string]int32{
		"doubles": 2,
		"triples": 3,
		"quads":   4,
		"quints":  5,
		"sexts":   6,
	}

	if len(digitRolls) != len(want) {
		t.Fatalf("digitRolls has %d entries, want %d", len(digitRolls), len(want))
	}

	for _, roll := range digitRolls {
		wantDigits, known := want[roll.name]
		if !known {
			t.Errorf("unexpected digit roll %q", roll.name)
			continue
		}
		if roll.digits != wantDigits {
			t.Errorf("%s digits = %d, want %d", roll.name, roll.digits, wantDigits)
		}
	}
}

// TestApplicationCommandsDerivedFromRegistry covers the acceptance criterion
// that the Discord definitions are generated, not hand-written: a command that
// exists in the registry must appear, with its arguments, in the slice sent to
// Discord.
func TestApplicationCommandsDerivedFromRegistry(t *testing.T) {
	registry := newTestRegistry(t)
	applications := applicationCommands(registry)

	if len(applications) != len(registry.All()) {
		t.Fatalf("generated %d application commands, want %d", len(applications), len(registry.All()))
	}

	byName := make(map[string]*discordgo.ApplicationCommand, len(applications))
	for _, application := range applications {
		byName[application.Name] = application
	}

	for _, cmd := range registry.All() {
		application, ok := byName[cmd.Name]
		if !ok {
			t.Errorf("command %q is registered but not sent to Discord", cmd.Name)
			continue
		}
		if application.Description != cmd.Description {
			t.Errorf("%s description = %q, want %q", cmd.Name, application.Description, cmd.Description)
		}
		if len(application.Options) != len(cmd.Args) {
			t.Errorf("%s has %d options, want %d", cmd.Name, len(application.Options), len(cmd.Args))
		}
	}
}

// TestApplicationCommandsPreserveLocalizations guards the migration off the
// hand-written EntertainmentCommands and UtilityCommands slices. The
// translations were not supposed to change, and nothing else would notice if
// they silently vanished.
func TestApplicationCommandsPreserveLocalizations(t *testing.T) {
	applications := applicationCommands(newTestRegistry(t))

	byName := make(map[string]*discordgo.ApplicationCommand, len(applications))
	for _, application := range applications {
		byName[application.Name] = application
	}

	tests := []struct {
		command string
		locale  discordgo.Locale
		want    string
	}{
		{command: "healthcheck", locale: discordgo.Japanese, want: "ヘルスチェック"},
		{command: "doubles", locale: discordgo.Finnish, want: "tuplat"},
		{command: "triples", locale: discordgo.Finnish, want: "triplat"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			application, ok := byName[tt.command]
			if !ok {
				t.Fatalf("command %q was not generated", tt.command)
			}
			if application.NameLocalizations == nil {
				t.Fatalf("command %q lost its name localizations", tt.command)
			}
			if got := (*application.NameLocalizations)[tt.locale]; got != tt.want {
				t.Errorf("%s[%s] = %q, want %q", tt.command, tt.locale, got, tt.want)
			}
		})
	}
}

// TestLocalizedNamesResolveAsAliases covers the chat side of the same thing:
// ??tuplat must reach doubles, because a Finnish user who uses /tuplat will
// expect it to.
func TestLocalizedNamesResolveAsAliases(t *testing.T) {
	registry := newTestRegistry(t)

	tests := []struct {
		alias string
		want  string
	}{
		{alias: "tuplat", want: "doubles"},
		{alias: "TUPLAT", want: "doubles"},
		{alias: "triplat", want: "triples"},
		{alias: "ヘルスチェック", want: "healthcheck"},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			cmd, ok := registry.Lookup(tt.alias)
			if !ok {
				t.Fatalf("alias %q does not resolve", tt.alias)
			}
			if cmd.Name != tt.want {
				t.Errorf("alias %q resolved to %q, want %q", tt.alias, cmd.Name, tt.want)
			}
		})
	}
}

// TestLocalizationsMatchRegisteredCommands catches a rename. Both the alias and
// the Discord translation are looked up by canonical name, so renaming a
// command without renaming its localization key drops both silently.
func TestLocalizationsMatchRegisteredCommands(t *testing.T) {
	registry := newTestRegistry(t)

	for name := range commandLocalizations {
		if _, ok := registry.Lookup(name); !ok {
			t.Errorf("commandLocalizations has key %q, which is not a registered command", name)
		}
	}
}

func TestApplicationCommandOptionType(t *testing.T) {
	tests := []struct {
		name string
		arg  command.ArgType
		want discordgo.ApplicationCommandOptionType
	}{
		{name: "string", arg: command.ArgString, want: discordgo.ApplicationCommandOptionString},
		{name: "int", arg: command.ArgInt, want: discordgo.ApplicationCommandOptionInteger},
		{name: "bool", arg: command.ArgBool, want: discordgo.ApplicationCommandOptionBoolean},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applicationCommandOptionType(tt.arg); got != tt.want {
				t.Errorf("applicationCommandOptionType(%v) = %v, want %v", tt.arg, got, tt.want)
			}
		})
	}
}
