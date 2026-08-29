package discord

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
)

// newTestRegistry builds the real command set; handlers are never invoked, so no
// client or session is needed.
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

// TestCommandDefinitionsRegister is the registration check initCommands makes at
// startup, as a test failure rather than a boot-time log.Z.Fatal.
func TestCommandDefinitionsRegister(t *testing.T) {
	registry := newTestRegistry(t)

	// Exact, not a subset: a command silently disappearing is the regression.
	want := []string{
		"doubles", "healthcheck", "help", "info", "locale", "number", "ping",
		"quads", "quints", "register", "remind", "reminderdel", "reminderinfo",
		"remindermod", "reminders", "sexts", "timezone", "triggeradd",
		"triggerdel", "triggerexec", "triggerinfo", "triggermod", "triggers",
		"triggerstats", "triples", "userinfo",
	}

	got := make([]string, 0, len(want))
	for _, cmd := range registry.All() {
		got = append(got, cmd.Name)
	}

	if !slices.Equal(got, want) {
		t.Errorf("registered commands = %q, want %q", got, want)
	}
}

// TestApplicationCommandsRespectDiscordLimits: a description exceeding Discord's
// limits fails ApplicationCommandCreate with a 400 at boot; this catches it here.
func TestApplicationCommandsRespectDiscordLimits(t *testing.T) {
	if err := validateApplicationCommands(applicationCommands(newTestRegistry(t))); err != nil {
		t.Errorf("generated commands violate Discord's limits: %v", err)
	}
}

func subCommandNamed(name, description string) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Name:        name,
		Description: description,
		Type:        discordgo.ApplicationCommandOptionSubCommand,
	}
}

// groupWith builds a group parent carrying the given subcommands.
func groupWith(subs ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        "reminder",
		Description: "fine",
		Options:     subs,
	}
}

// repeatedSubCommands builds n valid subcommands, so only the count can fail.
func repeatedSubCommands(n int) []*discordgo.ApplicationCommandOption {
	subs := make([]*discordgo.ApplicationCommandOption, 0, n)
	for i := range n {
		subs = append(subs, subCommandNamed(fmt.Sprintf("sub%d", i), "fine"))
	}

	return subs
}

// TestValidateApplicationCommandsCatchesViolations proves the validator can fail.
func TestValidateApplicationCommandsCatchesViolations(t *testing.T) {
	tests := []struct {
		name        string
		application *discordgo.ApplicationCommand
	}{
		{
			name: "option description too long",
			application: &discordgo.ApplicationCommand{
				Name:        "ok",
				Description: "fine",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Name:        "repeat",
						Description: strings.Repeat("x", maxOptionDescription+1),
						Type:        discordgo.ApplicationCommandOptionString,
					},
				},
			},
		},
		{
			name: "command description too long",
			application: &discordgo.ApplicationCommand{
				Name:        "ok",
				Description: strings.Repeat("x", maxCommandDescription+1),
			},
		},
		{
			name: "empty command description",
			application: &discordgo.ApplicationCommand{
				Name:        "ok",
				Description: "",
			},
		},
		{
			name: "command name too long",
			application: &discordgo.ApplicationCommand{
				Name:        strings.Repeat("x", maxCommandName+1),
				Description: "fine",
			},
		},
		{
			name: "uppercase command name",
			application: &discordgo.ApplicationCommand{
				Name:        "Remind",
				Description: "fine",
			},
		},
		{
			name: "empty option description",
			application: &discordgo.ApplicationCommand{
				Name:        "ok",
				Description: "fine",
				Options: []*discordgo.ApplicationCommandOption{
					{Name: "when", Description: "", Type: discordgo.ApplicationCommandOptionString},
				},
			},
		},
		{
			// A subcommand is an option, so the same limits apply one level down.
			name:        "subcommand description too long",
			application: groupWith(subCommandNamed("add", strings.Repeat("x", maxOptionDescription+1))),
		},
		{
			name:        "subcommand name too long",
			application: groupWith(subCommandNamed(strings.Repeat("x", maxOptionName+1), "fine")),
		},
		{
			name:        "empty subcommand name",
			application: groupWith(subCommandNamed("", "fine")),
		},
		{
			name:        "empty subcommand description",
			application: groupWith(subCommandNamed("add", "")),
		},
		{
			name:        "uppercase subcommand name",
			application: groupWith(subCommandNamed("Add", "fine")),
		},
		{
			// A subcommand's own options are what a slash invocation carries.
			name: "an argument of a subcommand has a too-long description",
			application: groupWith(&discordgo.ApplicationCommandOption{
				Name:        "add",
				Description: "fine",
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{
						Name:        "when",
						Description: strings.Repeat("x", maxOptionDescription+1),
						Type:        discordgo.ApplicationCommandOptionString,
					},
				},
			}),
		},
		{
			name: "an argument of a subcommand has an uppercase name",
			application: groupWith(&discordgo.ApplicationCommandOption{
				Name:        "add",
				Description: "fine",
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{Name: "When", Description: "fine", Type: discordgo.ApplicationCommandOptionString},
				},
			}),
		},
		{
			name:        "too many subcommands in one group",
			application: groupWith(repeatedSubCommands(maxOptionsPerCommand + 1)...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateApplicationCommands([]*discordgo.ApplicationCommand{tt.application}); err == nil {
				t.Error("expected a validation error, got nil")
			}
		})
	}
}

// TestDigitRollDigits pins the digit counts; the server zero-pads to this width.
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

func generatedByName(t *testing.T, registry *command.Registry) map[string]*discordgo.ApplicationCommand {
	t.Helper()

	applications := applicationCommands(registry)
	byName := make(map[string]*discordgo.ApplicationCommand, len(applications))
	for _, application := range applications {
		if _, duplicate := byName[application.Name]; duplicate {
			t.Fatalf("two application commands are named %q", application.Name)
		}
		byName[application.Name] = application
	}

	return byName
}

func subCommandOf(parent *discordgo.ApplicationCommand, sub string) (*discordgo.ApplicationCommandOption, bool) {
	for _, option := range parent.Options {
		if option.Name == sub {
			return option, true
		}
	}

	return nil, false
}

// TestApplicationCommandsDerivedFromRegistry: every registered command appears,
// with its arguments, in the slice sent to Discord; grouped ones as subcommands.
func TestApplicationCommandsDerivedFromRegistry(t *testing.T) {
	registry := newTestRegistry(t)
	byName := generatedByName(t, registry)

	wantTopLevel := 0
	for _, cmd := range registry.All() {
		if cmd.Group == "" {
			wantTopLevel++
		}
	}
	wantTopLevel += len(registry.Groups())

	if len(byName) != wantTopLevel {
		t.Fatalf("generated %d application commands, want %d", len(byName), wantTopLevel)
	}

	for _, cmd := range registry.All() {
		if cmd.Group != "" {
			parent, ok := byName[cmd.Group]
			if !ok {
				t.Errorf("command %q is in group %q, which was not generated", cmd.Name, cmd.Group)
				continue
			}
			sub, ok := subCommandOf(parent, cmd.Sub)
			if !ok {
				t.Errorf("command %q is registered but %q %q was not generated", cmd.Name, cmd.Group, cmd.Sub)
				continue
			}
			if sub.Description != cmd.Description {
				t.Errorf("%s %s description = %q, want %q", cmd.Group, cmd.Sub, sub.Description, cmd.Description)
			}
			if len(sub.Options) != len(cmd.Args) {
				t.Errorf("%s %s has %d options, want %d", cmd.Group, cmd.Sub, len(sub.Options), len(cmd.Args))
			}
			continue
		}

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

// TestReminderCommandsGenerateOneGroupedCommand: five registered commands become
// one top-level command with five subcommands.
func TestReminderCommandsGenerateOneGroupedCommand(t *testing.T) {
	registry := newTestRegistry(t)
	byName := generatedByName(t, registry)

	parent, ok := byName[reminderGroup]
	if !ok {
		t.Fatalf("no %q command was generated", reminderGroup)
	}
	if parent.Description == "" {
		t.Error("the reminder group parent has no description; Discord rejects that")
	}

	wantSubs := []string{
		reminderSubAdd, reminderSubDel, reminderSubInfo, reminderSubMod, reminderSubList,
	}

	gotSubs := make([]string, 0, len(parent.Options))
	for _, option := range parent.Options {
		gotSubs = append(gotSubs, option.Name)
		// Discord rejects a command mixing subcommands with ordinary options.
		if option.Type != discordgo.ApplicationCommandOptionSubCommand {
			t.Errorf("%q option %q has type %v, want SubCommand", reminderGroup, option.Name, option.Type)
		}
	}

	slices.Sort(gotSubs)
	slices.Sort(wantSubs)
	if !slices.Equal(gotSubs, wantSubs) {
		t.Errorf("%q subcommands = %q, want %q", reminderGroup, gotSubs, wantSubs)
	}

	// No member may also exist at top level.
	for _, name := range []string{
		"remind", "reminders", "reminderdel", "remindermod", "reminderinfo",
	} {
		if _, top := byName[name]; top {
			t.Errorf("%q is still generated as a top-level command", name)
		}
	}
}

// TestSubCommandCarriesItsOwnArguments: a slash invocation delivers a
// subcommand's arguments as the subcommand's own options.
func TestSubCommandCarriesItsOwnArguments(t *testing.T) {
	byName := generatedByName(t, newTestRegistry(t))

	parent, ok := byName[reminderGroup]
	if !ok {
		t.Fatalf("no %q command was generated", reminderGroup)
	}

	tests := []struct {
		sub  string
		cmd  command.Command
		want []string
	}{
		{sub: reminderSubAdd, cmd: remindCommand(), want: []string{"when", "message", "repeat"}},
		{sub: reminderSubMod, cmd: reminderModCommand(), want: []string{"id", "when", "message", "repeat"}},
		{sub: reminderSubInfo, cmd: reminderInfoCommand(), want: []string{"id"}},
		{sub: reminderSubDel, cmd: reminderDelCommand(), want: []string{"ids"}},
		{sub: reminderSubList, cmd: remindersCommand(), want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			sub, found := subCommandOf(parent, tt.sub)
			if !found {
				t.Fatalf("%q has no %q subcommand", reminderGroup, tt.sub)
			}

			got := make([]string, 0, len(sub.Options))
			for _, option := range sub.Options {
				got = append(got, option.Name)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("%q %q options = %q, want %q", reminderGroup, tt.sub, got, tt.want)
			}

			for i, arg := range tt.cmd.Args {
				option := sub.Options[i]
				if option.Type != applicationCommandOptionType(arg.Type) {
					t.Errorf("%q type = %v, want %v", arg.Name, option.Type,
						applicationCommandOptionType(arg.Type))
				}
				if option.Required != arg.Required {
					t.Errorf("%q required = %v, want %v", arg.Name, option.Required, arg.Required)
				}
				if option.Description != arg.Description {
					t.Errorf("%q description = %q, want %q", arg.Name, option.Description, arg.Description)
				}
			}
		})
	}
}

// TestGroupParentCarriesNoTopLevelOptions: Discord rejects a command mixing
// subcommands with ordinary options, so a group parent is only a container.
func TestGroupParentCarriesNoTopLevelOptions(t *testing.T) {
	registry := newTestRegistry(t)
	byName := generatedByName(t, registry)

	for _, group := range registry.Groups() {
		parent, ok := byName[group]
		if !ok {
			t.Errorf("group %q was not generated", group)
			continue
		}
		if len(parent.Options) == 0 {
			t.Errorf("group %q has no subcommands", group)
		}
		for _, option := range parent.Options {
			if option.Type != discordgo.ApplicationCommandOptionSubCommand {
				t.Errorf("group %q carries a non-subcommand option %q of type %v",
					group, option.Name, option.Type)
			}
		}
	}
}

// TestUngroupedCommandsStayTopLevel: grouping the reminders moved nothing else.
func TestUngroupedCommandsStayTopLevel(t *testing.T) {
	registry := newTestRegistry(t)
	byName := generatedByName(t, registry)

	for _, cmd := range registry.All() {
		if cmd.Group != "" {
			continue
		}
		application, ok := byName[cmd.Name]
		if !ok {
			t.Errorf("ungrouped command %q is no longer generated at top level", cmd.Name)
			continue
		}
		if len(application.Options) != len(cmd.Args) {
			t.Errorf("%s has %d options, want %d", cmd.Name, len(application.Options), len(cmd.Args))
			continue
		}
		for i, arg := range cmd.Args {
			option := application.Options[i]
			if option.Name != arg.Name {
				t.Errorf("%s option %d = %q, want %q", cmd.Name, i, option.Name, arg.Name)
			}
			if option.Type != applicationCommandOptionType(arg.Type) {
				t.Errorf("%s option %q type = %v, want %v", cmd.Name, arg.Name, option.Type,
					applicationCommandOptionType(arg.Type))
			}
			if option.Type == discordgo.ApplicationCommandOptionSubCommand {
				t.Errorf("%s option %q is a subcommand; the command is not grouped", cmd.Name, arg.Name)
			}
		}
	}
}

// TestEveryGroupHasADescription is the group-description check initCommands makes
// at startup; a group parent has no Command to carry its description.
func TestEveryGroupHasADescription(t *testing.T) {
	registry := newTestRegistry(t)

	groups := registry.Groups()
	if len(groups) == 0 {
		t.Fatal("no groups are registered; the check would pass vacuously")
	}

	for _, group := range groups {
		if commandGroupDescriptions[group] == "" {
			t.Errorf("group %q has no entry in commandGroupDescriptions", group)
		}
	}

	// A renamed group would leave a description keyed by a name nothing uses.
	for group := range commandGroupDescriptions {
		if !slices.Contains(groups, group) {
			t.Errorf("commandGroupDescriptions has key %q, which is not a registered group", group)
		}
	}
}

// TestGroupedCommandsHaveNoLocalizations: commandLocalizations translates a
// flat name, but a subcommand's Discord name is its Sub, so it cannot apply.
func TestGroupedCommandsHaveNoLocalizations(t *testing.T) {
	for _, cmd := range newTestRegistry(t).All() {
		if cmd.Group == "" {
			continue
		}
		if _, localized := commandLocalizations[cmd.Name]; localized {
			t.Errorf("grouped command %q has a localization, which the generated subcommand cannot carry", cmd.Name)
		}
	}
}

// TestChatResolvesEveryReminderSubcommand: /reminder add and ??reminder add must
// reach the same handler.
func TestChatResolvesEveryReminderSubcommand(t *testing.T) {
	registry := newTestRegistry(t)

	tests := []struct {
		sub  string
		want string
	}{
		{sub: reminderSubAdd, want: "remind"},
		{sub: reminderSubList, want: "reminders"},
		{sub: reminderSubDel, want: "reminderdel"},
		{sub: reminderSubMod, want: "remindermod"},
		{sub: reminderSubInfo, want: "reminderinfo"},
	}

	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			cmd, rest, ok := registry.ResolveChat(reminderGroup, []string{tt.sub, "in 2h", "tea"})
			if !ok {
				t.Fatalf("??%s %s does not resolve", reminderGroup, tt.sub)
			}
			if cmd.Name != tt.want {
				t.Errorf("??%s %s resolved to %q, want %q", reminderGroup, tt.sub, cmd.Name, tt.want)
			}
			if !slices.Equal(rest, []string{"in 2h", "tea"}) {
				t.Errorf("remaining arguments = %q, want the two after the subcommand", rest)
			}
		})
	}
}

// TestApplicationCommandsPreserveLocalizations: the translations survive
// generation.
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

// TestLocalizedNamesResolveAsAliases: ??tuplat must reach doubles.
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

// TestLegacyReminderNamesResolve pins the previous bot's names; an unknown chat
// command is dropped silently.
func TestLegacyReminderNamesResolve(t *testing.T) {
	registry := newTestRegistry(t)

	tests := []struct {
		alias string
		want  string
	}{
		{alias: "remindme", want: "remind"},
		{alias: "reminderadd", want: "remind"},
		{alias: "reminderdetails", want: "reminderinfo"},
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

// TestLocalizationsMatchRegisteredCommands: a localization key not matching a
// registered command means a rename dropped both silently.
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
