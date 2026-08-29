package discord

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// Built in InitializeDiscord, not at package init: registration failures are
// reported with log.Z, which is nil until log.InitializeLogger has run.
var commandRegistry *command.Registry

// Discord-only translations, which the platform-neutral registry does not model.
type localization struct {
	names        map[discordgo.Locale]string
	descriptions map[discordgo.Locale]string
}

var commandLocalizations = map[string]localization{
	"healthcheck": {
		names:        map[discordgo.Locale]string{discordgo.Japanese: "ヘルスチェック"},
		descriptions: map[discordgo.Locale]string{discordgo.Japanese: "DBなどのサービスのヘルスチェック。"},
	},
	"doubles": {
		names: map[discordgo.Locale]string{discordgo.Finnish: "tuplat"},
	},
	"triples": {
		names: map[discordgo.Locale]string{discordgo.Finnish: "triplat"},
	},
}

// A Discord group parent is an ApplicationCommand needing its own description,
// but a group is only a name in the neutral registry. initCommands aborts
// startup for a group missing here.
var commandGroupDescriptions = map[string]string{
	reminderGroup: "Create and manage your reminders",
	triggerGroup:  "Create and manage auto-responder triggers",
}

// Discord's limits on a chat-input command. Exceeding one fails
// ApplicationCommandCreate with an HTTP 400 naming only an option index.
// https://discord.com/developers/docs/interactions/application-commands
const (
	maxCommandName        = 32
	maxCommandDescription = 100
	maxOptionName         = 32
	maxOptionDescription  = 100
	maxOptionsPerCommand  = 25
)

// A collision or malformed command aborts startup: it would otherwise register
// a command that never responds.
func initCommands() {
	registry := command.NewRegistry()

	for _, cmd := range commandDefinitions() {
		if err := registry.Register(cmd); err != nil {
			log.Z.Fatal("cannot register command.", zap.String("command", cmd.Name), zap.Error(err))
		}
	}

	// Nothing else notices a missing description until Discord rejects it.
	for _, group := range registry.Groups() {
		if commandGroupDescriptions[group] == "" {
			log.Z.Fatal("command group has no description.", zap.String("group", group))
		}
	}

	// Before the session opens, so this beats a 400 partway through registering.
	if err := validateApplicationCommands(applicationCommands(registry)); err != nil {
		log.Z.Fatal("generated command definitions are invalid.", zap.Error(err))
	}

	commandRegistry = registry
}

// Definitions are derived from the registry (ADR-0009), where no Discord
// constraint is visible. This turns Discord's `options.3.description` into a
// named failure a test can catch before a live start.
func validateApplicationCommands(applications []*discordgo.ApplicationCommand) error {
	for _, application := range applications {
		if n := len(application.Name); n == 0 || n > maxCommandName {
			return fmt.Errorf("command %q: name is %d characters, want 1-%d",
				application.Name, n, maxCommandName)
		}
		// Discord requires a chat-input command name to be lowercase.
		if application.Name != strings.ToLower(application.Name) {
			return fmt.Errorf("command %q: name must be lowercase", application.Name)
		}
		if n := len(application.Description); n == 0 || n > maxCommandDescription {
			return fmt.Errorf("command %q: description is %d characters, want 1-%d",
				application.Name, n, maxCommandDescription)
		}
		// A group parent's options are its subcommands, so this is also the
		// 25-subcommands-per-group limit.
		if n := len(application.Options); n > maxOptionsPerCommand {
			return fmt.Errorf("command %q: has %d options, want at most %d",
				application.Name, n, maxOptionsPerCommand)
		}

		path := fmt.Sprintf("command %q", application.Name)
		for _, option := range application.Options {
			if err := validateApplicationCommandOption(path, option); err != nil {
				return err
			}
		}
	}

	return nil
}

// path accumulates the enclosing names, so a failure Discord would report as
// `options.3.options.1.description` can name the field instead.
func validateApplicationCommandOption(path string, option *discordgo.ApplicationCommandOption) error {
	if n := len(option.Name); n == 0 || n > maxOptionName {
		return fmt.Errorf("%s option %q: name is %d characters, want 1-%d",
			path, option.Name, n, maxOptionName)
	}
	if option.Name != strings.ToLower(option.Name) {
		return fmt.Errorf("%s option %q: name must be lowercase", path, option.Name)
	}
	if n := len(option.Description); n == 0 || n > maxOptionDescription {
		return fmt.Errorf("%s option %q: description is %d characters, want 1-%d",
			path, option.Name, n, maxOptionDescription)
	}
	if n := len(option.Options); n > maxOptionsPerCommand {
		return fmt.Errorf("%s option %q: has %d options, want at most %d",
			path, option.Name, n, maxOptionsPerCommand)
	}

	nestedPath := fmt.Sprintf("%s option %q", path, option.Name)
	for _, nested := range option.Options {
		if err := validateApplicationCommandOption(nestedPath, nested); err != nil {
			return err
		}
	}

	return nil
}

func commandDefinitions() []command.Command {
	definitions := []command.Command{
		healthCheckCommand(),
		helpCommand(),
		infoCommand(),
		localeCommand(),
		numberCommand(),
		pingCommand(),
		registerCommand(),
		remindCommand(),
		remindersCommand(),
		reminderDelCommand(),
		reminderInfoCommand(),
		reminderModCommand(),
		timezoneCommand(),
		triggerAddCommand(),
		triggerDelCommand(),
		triggerExecCommand(),
		triggerInfoCommand(),
		triggerListCommand(),
		triggerModCommand(),
		triggerStatsCommand(),
		userInfoCommand(),
	}

	return append(definitions, digitRollCommands()...)
}

// Lets a localised slash name work as a chat command too, so ??tuplat behaves
// like ??doubles.
func localizedAliases(name string) []string {
	names := commandLocalizations[name].names
	if len(names) == 0 {
		return nil
	}

	aliases := make([]string, 0, len(names))
	for _, localized := range names {
		aliases = append(aliases, localized)
	}
	// Sorted, so the registered command is identical on every start.
	slices.Sort(aliases)

	return aliases
}

// Commands sharing a Group collapse into one ApplicationCommand named after the
// group, whose options are SubCommand entries. Registry.All is ordered by name,
// so parent position and subcommand order are identical on every start.
func applicationCommands(registry *command.Registry) []*discordgo.ApplicationCommand {
	all := registry.All()
	applications := make([]*discordgo.ApplicationCommand, 0, len(all))
	parents := make(map[string]*discordgo.ApplicationCommand)

	for _, cmd := range all {
		if cmd.Group == "" {
			applications = append(applications, topLevelApplicationCommand(cmd))
			continue
		}

		// Keyed folded: the registry treats "reminder" and "Reminder" as one
		// group, and keying verbatim would emit two parents for it.
		key := strings.ToLower(cmd.Group)

		parent, ok := parents[key]
		if !ok {
			// No Options beyond the subcommands: Discord rejects a command that
			// mixes subcommands with ordinary options. Name is the group as
			// declared, so a non-lowercase one is reported rather than corrected.
			parent = &discordgo.ApplicationCommand{
				Name:        cmd.Group,
				Description: commandGroupDescriptions[cmd.Group],
			}
			parents[key] = parent
			applications = append(applications, parent)
		}

		parent.Options = append(parent.Options, subCommandOption(cmd))
	}

	return applications
}

func topLevelApplicationCommand(cmd command.Command) *discordgo.ApplicationCommand {
	application := &discordgo.ApplicationCommand{
		Name:        cmd.Name,
		Description: cmd.Description,
		Options:     applicationCommandOptions(cmd.Args),
	}

	if localized, ok := commandLocalizations[cmd.Name]; ok {
		if len(localized.names) > 0 {
			application.NameLocalizations = &localized.names
		}
		if len(localized.descriptions) > 0 {
			application.DescriptionLocalizations = &localized.descriptions
		}
	}

	return application
}

// commandLocalizations is deliberately not consulted: its names translate a
// command's flat Name, but a subcommand's Discord name is Sub.
func subCommandOption(cmd command.Command) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Name:        cmd.Sub,
		Description: cmd.Description,
		Type:        discordgo.ApplicationCommandOptionSubCommand,
		Options:     applicationCommandOptions(cmd.Args),
	}
}

func applicationCommandOptions(args []command.Arg) []*discordgo.ApplicationCommandOption {
	if len(args) == 0 {
		return nil
	}

	options := make([]*discordgo.ApplicationCommandOption, 0, len(args))
	for _, arg := range args {
		options = append(options, &discordgo.ApplicationCommandOption{
			Name:        arg.Name,
			Description: arg.Description,
			Type:        applicationCommandOptionType(arg.Type),
			Required:    arg.Required,
		})
	}

	return options
}

func applicationCommandOptionType(argType command.ArgType) discordgo.ApplicationCommandOptionType {
	switch argType {
	case command.ArgInt:
		return discordgo.ApplicationCommandOptionInteger
	case command.ArgBool:
		return discordgo.ApplicationCommandOptionBoolean
	default:
		return discordgo.ApplicationCommandOptionString
	}
}
