package discord

import (
	"fmt"
	"slices"
	"strings"
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

	// Exact, not a subset: a command silently disappearing is exactly the kind
	// of regression this catches, so extending the bot means extending this list
	// deliberately.
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

// TestApplicationCommandsRespectDiscordLimits is the regression test for a live
// boot failure: remindermod's repeat description was 120 characters against
// Discord's 100 limit, so ApplicationCommandCreate returned HTTP 400 and the
// bot died at startup — reporting only `options.3.description`, with no command
// name.
//
// Descriptions are written in the registry, where no Discord constraint is
// visible, so nothing else stops one growing. This asserts the real generated
// definitions, so it fails here rather than against the live API.
func TestApplicationCommandsRespectDiscordLimits(t *testing.T) {
	if err := validateApplicationCommands(applicationCommands(newTestRegistry(t))); err != nil {
		t.Errorf("generated commands violate Discord's limits: %v", err)
	}
}

// subCommandNamed builds one SubCommand-typed option, with no arguments.
func subCommandNamed(name, description string) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Name:        name,
		Description: description,
		Type:        discordgo.ApplicationCommandOptionSubCommand,
	}
}

// groupWith builds a group parent carrying the given subcommands, i.e. the shape
// applicationCommands generates for a grouped command.
func groupWith(subs ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        "reminder",
		Description: "fine",
		Options:     subs,
	}
}

// repeatedSubCommands builds n distinctly named, otherwise valid subcommands, so
// that only the count can be what a validator objects to.
func repeatedSubCommands(n int) []*discordgo.ApplicationCommandOption {
	subs := make([]*discordgo.ApplicationCommandOption, 0, n)
	for i := range n {
		subs = append(subs, subCommandNamed(fmt.Sprintf("sub%d", i), "fine"))
	}

	return subs
}

// TestValidateApplicationCommandsCatchesViolations proves the check above can
// actually fail — a validator that always returns nil would pass it silently.
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
			// A subcommand IS an option, so the same limits apply to it — and a
			// group's description lives one level down from where the pre-group
			// validator looked.
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
			// A subcommand's OWN options are what a slash invocation actually
			// carries, so an over-long one there breaks boot exactly as a
			// top-level option would — and nothing looked at them before.
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

// generatedByName indexes the generated definitions for lookup by Discord name.
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

// subCommandOf returns a group parent's named subcommand.
func subCommandOf(parent *discordgo.ApplicationCommand, sub string) (*discordgo.ApplicationCommandOption, bool) {
	for _, option := range parent.Options {
		if option.Name == sub {
			return option, true
		}
	}

	return nil, false
}

// TestApplicationCommandsDerivedFromRegistry covers the acceptance criterion
// that the Discord definitions are generated, not hand-written: a command that
// exists in the registry must appear, with its arguments, in the slice sent to
// Discord.
//
// A grouped command appears as a subcommand of its group rather than at top
// level, so the assertion follows the shape rather than assuming one
// ApplicationCommand per registered command.
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

// TestReminderCommandsGenerateOneGroupedCommand is the /reminder shape: five
// registered commands, ONE top-level Discord command, five subcommands. Five
// top-level reminder commands is what this replaces, so a member escaping back
// to top level is the regression to catch.
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
		// A group parent is purely a container: Discord rejects a command that
		// mixes subcommands with ordinary options.
		if option.Type != discordgo.ApplicationCommandOptionSubCommand {
			t.Errorf("%q option %q has type %v, want SubCommand", reminderGroup, option.Name, option.Type)
		}
	}

	slices.Sort(gotSubs)
	slices.Sort(wantSubs)
	if !slices.Equal(gotSubs, wantSubs) {
		t.Errorf("%q subcommands = %q, want %q", reminderGroup, gotSubs, wantSubs)
	}

	// None of the members may also exist at top level.
	for _, name := range []string{
		"remind", "reminders", "reminderdel", "remindermod", "reminderinfo",
	} {
		if _, top := byName[name]; top {
			t.Errorf("%q is still generated as a top-level command", name)
		}
	}
}

// TestSubCommandCarriesItsOwnArguments: a slash invocation delivers a
// subcommand's arguments as the SUBCOMMAND's options, so that is where they have
// to be declared. Putting them on the parent would both be rejected by Discord
// and leave the handler with nothing bound.
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

			// Type and requiredness are derived from the declared Arg, so a
			// slash option and a chat positional cannot disagree.
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

// TestGroupParentCarriesNoTopLevelOptions: Discord rejects a chat-input command
// that mixes subcommands with ordinary options, so a group parent must be a
// container and nothing else. It has no Args of its own to derive one from
// either — there is no Command behind a group.
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

// TestUngroupedCommandsStayTopLevel: grouping the reminders was not supposed to
// move anything else. Games and tools are a separate decision.
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

// TestEveryGroupHasADescription is the check initCommands makes at startup,
// where the only report is log.Z.Fatal. A group parent is generated, so there is
// no Command to carry its description and nothing else would notice it missing
// until Discord rejected the empty one at boot.
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

	// The mirror of TestLocalizationsMatchRegisteredCommands: a renamed group
	// would otherwise leave a description keyed by a name nothing uses.
	for group := range commandGroupDescriptions {
		if !slices.Contains(groups, group) {
			t.Errorf("commandGroupDescriptions has key %q, which is not a registered group", group)
		}
	}
}

// TestGroupedCommandsHaveNoLocalizations pins the deliberate gap.
// commandLocalizations translates a command's FLAT name, and a subcommand's
// Discord name is its Sub, so subCommandOption cannot apply one without shipping
// a translation of the wrong token. Nothing is dropped today; the day someone
// adds a translation for a grouped command, this fails and the choice gets made
// on purpose.
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

// TestChatResolvesEveryReminderSubcommand ties the generated slash surface back
// to the chat one: /reminder add and ??reminder add must reach the same handler,
// and the legacy flat names must keep working alongside both.
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
			// The subcommand token is consumed, so binding sees the same
			// arguments the legacy flat invocation would.
			if !slices.Equal(rest, []string{"in 2h", "tea"}) {
				t.Errorf("remaining arguments = %q, want the two after the subcommand", rest)
			}
		})
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

// TestLegacyReminderNamesResolve pins the names the previous bot used. They are
// what people have in muscle memory, and dropping one would fail silently: an
// unknown chat command is ignored without a reply.
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
