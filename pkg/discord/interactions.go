package discord

import (
	"context"
	"errors"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reRollPrefix namespaces a re-roll button's custom ID so that the component
// handler can recover the command name and re-run it through the registry,
// instead of needing one hand-written entry per rollable command.
const reRollPrefix = "reroll:"

// legacyReRollIDs maps the hand-written custom IDs that shipped before the
// reroll: namespace existed. Discord components never expire, so every button
// on an already-posted message still sends the old ID; without this every one
// of them answers "This interaction failed" forever.
var legacyReRollIDs = map[string]string{
	"reRollDoubles": "doubles",
	"reRollTriples": "triples",
}

func reRollID(name string) string {
	return reRollPrefix + name
}

// reRollCommandName recovers the command a re-roll button refers to.
func reRollCommandName(customID string) (string, bool) {
	if name, found := strings.CutPrefix(customID, reRollPrefix); found {
		return name, true
	}

	name, found := legacyReRollIDs[customID]

	return name, found
}

// invokerKey types the context value carrying the Discord user who ran a
// command, so it cannot collide with another package's key.
type invokerKey struct{}

// invoker is the Discord user behind a command.
//
// It is deliberately not part of command.Invocation: identity belongs in gRPC
// metadata, and duplicating it into the neutral invocation would give handlers
// two sources of truth for who is calling. What metadata cannot carry is the
// display name, which registration needs and which only the platform knows —
// so it rides in the context, private to this package.
type invoker struct {
	ID       string
	Username string
}

func withInvoker(ctx context.Context, user *discordgo.User) context.Context {
	return context.WithValue(ctx, invokerKey{}, invoker{ID: user.ID, Username: user.Username})
}

// invokerFromContext returns the Discord user behind the current command.
func invokerFromContext(ctx context.Context) (invoker, bool) {
	user, ok := ctx.Value(invokerKey{}).(invoker)
	return user, ok
}

// originKey types the context value carrying where a command was typed, so the
// reminder commands can build the ReminderDestination for the current channel.
type originKey struct{}

// commandOrigin is the guild and channel a command was invoked in. A direct
// message has an empty GuildID.
type commandOrigin struct {
	GuildID   string
	ChannelID string
}

func withOrigin(ctx context.Context, guildID string, channelID string) context.Context {
	return context.WithValue(ctx, originKey{}, commandOrigin{GuildID: guildID, ChannelID: channelID})
}

// originFromContext returns where the current command was typed.
func originFromContext(ctx context.Context) (commandOrigin, bool) {
	origin, ok := ctx.Value(originKey{}).(commandOrigin)
	return origin, ok
}

// discordOrigin describes where a command was typed, so the server can create
// the instance and destination rows on first contact.
//
// A direct message has no GuildID; callermeta drops such an origin, because
// there is no guild to record.
func discordOrigin(guildID string, channelID string) callermeta.Origin {
	return callermeta.Origin{InstanceUID: guildID, DestinationUID: channelID}
}

// commandContext assembles the context every handler receives: caller identity
// and origin as gRPC metadata, plus the invoking user for the handlers that
// need a display name.
func commandContext(user *discordgo.User, guildID string, channelID string) context.Context {
	ctx := callermeta.NewOutgoingContext(context.Background(), pb.Platform_PLATFORM_DISCORD, user.ID)
	ctx = callermeta.NewOutgoingOrigin(ctx, discordOrigin(guildID, channelID))
	ctx = withOrigin(ctx, guildID, channelID)

	return withInvoker(ctx, user)
}

func interactionContext(i *discordgo.InteractionCreate) (context.Context, error) {
	var user *discordgo.User
	// Member.User is populated for guild interactions and User for DMs, but the
	// nested pointer is checked too: a nil deref here would kill the process,
	// because discordgo does not recover panics raised in a handler.
	if i.Member != nil && i.Member.User != nil {
		user = i.Member.User
	} else if i.User != nil {
		user = i.User
	} else {
		log.Z.Error("cannot get user id.")
		return context.Background(), errors.New("cannot get discord user id")
	}

	return commandContext(user, i.GuildID, i.ChannelID), nil
}

// messageContext builds the caller context for a chat command. Identity travels
// as gRPC metadata, never as a request field.
func messageContext(m *discordgo.MessageCreate) context.Context {
	return commandContext(m.Author, m.GuildID, m.ChannelID)
}

// handleInteraction routes slash commands and re-roll buttons through the
// registry.
func handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		data := i.ApplicationCommandData()

		cmd, options, ok := resolveApplicationCommand(data)
		if !ok {
			// resolveApplicationCommand has already logged why. It must still be
			// answered: an unanswered interaction shows the user "the application
			// did not respond".
			respondStale(s, i)
			return
		}

		inv, err := invocationFromOptions(cmd, options)
		if err != nil {
			respondError(s, i, err)
			return
		}

		runInteraction(s, i, cmd, inv)

	case discordgo.InteractionMessageComponent:
		customID := i.MessageComponentData().CustomID

		name, found := reRollCommandName(customID)
		if !found {
			log.Z.Warn("unknown message component.", zap.String("custom_id", customID))
			respondStale(s, i)
			return
		}

		cmd, ok := commandRegistry.Lookup(name)
		if !ok {
			log.Z.Warn("re-roll for unknown command.", zap.String("command", name))
			respondStale(s, i)
			return
		}

		// A re-roll carries no arguments; the defaults apply.
		inv, err := command.Bind(cmd, nil)
		if err != nil {
			respondError(s, i, err)
			return
		}

		runInteraction(s, i, cmd, inv)
	}
}

// resolveApplicationCommand resolves a slash invocation to the registered
// command and the options that carry ITS arguments.
//
// Discord flattens nothing: for /reminder add, data.Name is the GROUP and
// data.Options[0] is a SubCommand-typed option named after the member, whose own
// .Options are the arguments. So a grouped invocation lives one level deeper
// than a top-level one, and reading data.Options directly would bind the
// subcommand itself as an argument.
//
// ok is false for anything unroutable, always with a log line saying which
// shape it was — the caller answers the interaction rather than returning
// silently.
func resolveApplicationCommand(data discordgo.ApplicationCommandInteractionData) (command.Command, []*discordgo.ApplicationCommandInteractionDataOption, bool) {
	if cmd, found := commandRegistry.Lookup(data.Name); found {
		return cmd, data.Options, true
	}

	if !isCommandGroup(data.Name) {
		// Unreachable while the Discord definitions are generated from the
		// registry, but a stale command left over at Discord's end lands here.
		log.Z.Warn("unknown application command.", zap.String("command", data.Name))
		return command.Command{}, nil, false
	}

	if len(data.Options) == 0 {
		log.Z.Warn("group command carried no subcommand.", zap.String("command", data.Name))
		return command.Command{}, nil, false
	}

	sub := data.Options[0]
	if sub.Type != discordgo.ApplicationCommandOptionSubCommand {
		log.Z.Warn("group command did not begin with a subcommand.",
			zap.String("command", data.Name),
			zap.String("option", sub.Name),
			zap.Stringer("option_type", sub.Type))
		return command.Command{}, nil, false
	}

	// ResolveChat resolves group + sub, which is exactly this lookup; the chat
	// path and the slash path must not disagree about which handler /reminder add
	// reaches.
	cmd, _, found := commandRegistry.ResolveChat(data.Name, []string{sub.Name})
	if !found {
		log.Z.Warn("unknown subcommand.",
			zap.String("command", data.Name), zap.String("subcommand", sub.Name))
		return command.Command{}, nil, false
	}

	return cmd, sub.Options, true
}

// isCommandGroup reports whether a name is a registered group rather than a
// command. Group names fold like every other name in the registry.
func isCommandGroup(name string) bool {
	for _, group := range commandRegistry.Groups() {
		if strings.EqualFold(group, name) {
			return true
		}
	}

	return false
}

// runInteraction executes a command and renders its response to an interaction.
func runInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, cmd command.Command, inv *command.Invocation) {
	ctx, err := interactionContext(i)
	if err != nil {
		respondError(s, i, err)
		return
	}

	resp, err := cmd.Handler(ctx, inv)
	if err != nil {
		log.Z.Error("command failed.", zap.String("command", cmd.Name), zap.Error(err))
		respondError(s, i, err)
		return
	}

	respondCommand(s, i, resp)
}

// handleMessage routes a prefixed chat message through the registry.
func handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if !isHuman(s, m) {
		return
	}

	name, raw, ok := command.ParseChat(m.Content, config.Options.Discord.CommandPrefixes.Prefixes)
	if !ok {
		return
	}

	// ResolveChat rather than Lookup, so ??reminder add reaches the same handler
	// as ??remindme. It returns the arguments left after the command name — which
	// for a group is one token shorter than raw.
	cmd, args, found := commandRegistry.ResolveChat(name, raw)
	if !found {
		// Silent: a prefix is also used by other bots, and answering every typo
		// would make the bot noisy.
		return
	}

	inv, err := command.Bind(cmd, args)
	if err != nil {
		respondChatError(s, m, err)
		return
	}

	resp, err := cmd.Handler(messageContext(m), inv)
	if err != nil {
		log.Z.Error("chat command failed.", zap.String("command", cmd.Name), zap.Error(err))
		respondChatError(s, m, err)
		return
	}

	respondChat(s, m, resp)
}

// isHuman reports whether a message should be considered for dispatch. Ignoring
// bots covers this bot's own messages as well; the explicit self check guards
// against a future non-bot token and makes the intent obvious.
func isHuman(s *discordgo.Session, m *discordgo.MessageCreate) bool {
	if m.Author == nil || m.Author.Bot {
		return false
	}

	if s.State != nil && s.State.User != nil && m.Author.ID == s.State.User.ID {
		return false
	}

	return true
}

// invocationFromOptions translates Discord's typed slash options onto an
// Invocation. Options arrive named and already typed, so they bypass the chat
// tokeniser entirely.
func invocationFromOptions(cmd command.Command, options []*discordgo.ApplicationCommandInteractionDataOption) (*command.Invocation, error) {
	args := make(map[string]any, len(options))

	for _, option := range options {
		// Each accessor panics when the option is not of its type, and discordgo
		// dispatches handlers without recovering, so a panic here kills the
		// process. Every arm is matched explicitly rather than falling through to
		// StringValue, which would panic on any option type the registry learns to
		// emit later.
		switch option.Type {
		case discordgo.ApplicationCommandOptionInteger:
			args[option.Name] = option.IntValue()
		case discordgo.ApplicationCommandOptionBoolean:
			args[option.Name] = option.BoolValue()
		case discordgo.ApplicationCommandOptionString:
			args[option.Name] = option.StringValue()
		default:
			return nil, status.Errorf(codes.Internal, "unsupported option type %v for %q", option.Type, option.Name)
		}
	}

	return command.BindNamed(cmd, args)
}
