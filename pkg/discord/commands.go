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

// commandRegistry is the single source of truth for both invocation paths and
// for the generated slash command definitions.
//
// It is built in InitializeDiscord rather than at package init because
// registration failures are reported with log.Z, which is nil until
// log.InitializeLogger has run.
var commandRegistry *command.Registry

// localization carries the Discord-only translations that the platform-neutral
// registry does not model.
type localization struct {
	names        map[discordgo.Locale]string
	descriptions map[discordgo.Locale]string
}

// commandLocalizations holds only the translations that already existed. This
// phase adds none.
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

// Discord's limits on a chat-input command. Exceeding any of them fails
// ApplicationCommandCreate with an HTTP 400 that names only an option index, so
// they are checked here where the offending field can be named instead.
//
// https://discord.com/developers/docs/interactions/application-commands
const (
	maxCommandName        = 32
	maxCommandDescription = 100
	maxOptionName         = 32
	maxOptionDescription  = 100
	maxOptionsPerCommand  = 25
)

// initCommands builds the registry. A collision or a malformed command is a
// programming error that would otherwise register a command that never
// responds, so it aborts startup.
func initCommands() {
	registry := command.NewRegistry()

	for _, cmd := range commandDefinitions() {
		if err := registry.Register(cmd); err != nil {
			log.Z.Fatal("cannot register command.", zap.String("command", cmd.Name), zap.Error(err))
		}
	}

	// Checked before the session is opened, so a too-long description is a clear
	// message here rather than a Discord 400 partway through registering.
	if err := validateApplicationCommands(applicationCommands(registry)); err != nil {
		log.Z.Fatal("generated command definitions are invalid.", zap.Error(err))
	}

	commandRegistry = registry
}

// validateApplicationCommands checks the generated definitions against Discord's
// documented limits.
//
// Deriving the definitions from the registry (ADR-0009) means a command's
// description is written where no Discord constraint is visible, so nothing
// stopped one growing past the limit. Discord then rejects it at
// ApplicationCommandCreate with a fatal error at boot, and reports it as
// `options.3.description` — an index, with no command name. This turns that into
// a named failure, and lets a test catch it before it ever reaches a live start.
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
		if n := len(application.Options); n > maxOptionsPerCommand {
			return fmt.Errorf("command %q: has %d options, want at most %d",
				application.Name, n, maxOptionsPerCommand)
		}

		for _, option := range application.Options {
			if n := len(option.Name); n == 0 || n > maxOptionName {
				return fmt.Errorf("command %q option %q: name is %d characters, want 1-%d",
					application.Name, option.Name, n, maxOptionName)
			}
			if option.Name != strings.ToLower(option.Name) {
				return fmt.Errorf("command %q option %q: name must be lowercase",
					application.Name, option.Name)
			}
			if n := len(option.Description); n == 0 || n > maxOptionDescription {
				return fmt.Errorf("command %q option %q: description is %d characters, want 1-%d",
					application.Name, option.Name, n, maxOptionDescription)
			}
		}
	}

	return nil
}

// commandDefinitions lists every command the Discord client exposes.
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
		userInfoCommand(),
	}

	return append(definitions, digitRollCommands()...)
}

// localizedAliases lets a localised slash command name work as a chat command
// too, so that ??tuplat behaves like ??doubles. Discord has no alias concept of
// its own; the localised name is the closest existing equivalent.
func localizedAliases(name string) []string {
	names := commandLocalizations[name].names
	if len(names) == 0 {
		return nil
	}

	aliases := make([]string, 0, len(names))
	for _, localized := range names {
		aliases = append(aliases, localized)
	}
	// Map iteration order is random; sorted so the registered command is
	// identical on every start.
	slices.Sort(aliases)

	return aliases
}

// applicationCommands derives the Discord slash command definitions from the
// registry, so a command cannot be registered with Discord without a handler,
// nor gain a handler that Discord never routes to.
func applicationCommands(registry *command.Registry) []*discordgo.ApplicationCommand {
	all := registry.All()
	applications := make([]*discordgo.ApplicationCommand, 0, len(all))

	for _, cmd := range all {
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

		applications = append(applications, application)
	}

	return applications
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
