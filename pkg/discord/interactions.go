package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

const reRollPrefix = "reroll:"

// Discord components never expire, so buttons on already-posted messages still
// send these pre-namespace IDs.
var legacyReRollIDs = map[string]string{
	"reRollDoubles": "doubles",
	"reRollTriples": "triples",
}

func reRollID(name string) string {
	return reRollPrefix + name
}

func reRollCommandName(customID string) (string, bool) {
	if name, found := strings.CutPrefix(customID, reRollPrefix); found {
		return name, true
	}

	name, found := legacyReRollIDs[customID]

	return name, found
}

type invokerKey struct{}

// Kept out of command.Invocation: identity travels in request headers. Only the
// display name, which headers cannot carry, rides in the context.
type invoker struct {
	ID       string
	Username string
}

func withInvoker(ctx context.Context, user *discordgo.User) context.Context {
	return context.WithValue(ctx, invokerKey{}, invoker{ID: user.ID, Username: user.Username})
}

func invokerFromContext(ctx context.Context) (invoker, bool) {
	user, ok := ctx.Value(invokerKey{}).(invoker)
	return user, ok
}

type originKey struct{}

// commandOrigin is where a command was invoked. A DM has an empty GuildID.
type commandOrigin struct {
	GuildID   string
	ChannelID string
}

func withOrigin(ctx context.Context, guildID string, channelID string) context.Context {
	return context.WithValue(ctx, originKey{}, commandOrigin{GuildID: guildID, ChannelID: channelID})
}

func originFromContext(ctx context.Context) (commandOrigin, bool) {
	origin, ok := ctx.Value(originKey{}).(commandOrigin)
	return origin, ok
}

// A DM has no GuildID, and callermeta drops such an origin.
func discordOrigin(guildID string, channelID string) callermeta.Origin {
	return callermeta.Origin{InstanceUID: guildID, DestinationUID: channelID}
}

// commandContext assembles every handler's context. Caller identity and origin
// go in as request headers, never as request fields.
func commandContext(clients *client.Clients, user *discordgo.User, guildID string, channelID string) context.Context {
	ctx := callermeta.NewOutgoingContext(context.Background(), pb.Platform_PLATFORM_DISCORD, user.ID)
	ctx = callermeta.NewOutgoingOrigin(ctx, discordOrigin(guildID, channelID))
	ctx = withOrigin(ctx, guildID, channelID)
	ctx = withClients(ctx, clients)

	return withInvoker(ctx, user)
}

func interactionContext(i *discordgo.InteractionCreate, clients *client.Clients) (context.Context, error) {
	var user *discordgo.User
	// Member.User is populated for guild interactions, User for DMs.
	if i.Member != nil && i.Member.User != nil {
		user = i.Member.User
	} else if i.User != nil {
		user = i.User
	} else {
		log.Z.Error("cannot get user id.")
		return context.Background(), errors.New("cannot get discord user id")
	}

	return commandContext(clients, user, i.GuildID, i.ChannelID), nil
}

func messageContext(m *discordgo.MessageCreate, clients *client.Clients) context.Context {
	return commandContext(clients, m.Author, m.GuildID, m.ChannelID)
}

func handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, clients *client.Clients) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		data := i.ApplicationCommandData()

		cmd, options, ok := resolveApplicationCommand(data)
		if !ok {
			// An unanswered interaction shows "the application did not respond".
			respondStale(s, i)
			return
		}

		inv, err := invocationFromOptions(cmd, options)
		if err != nil {
			respondError(s, i, err)
			return
		}

		runInteraction(s, i, cmd, inv, clients)

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

		runInteraction(s, i, cmd, inv, clients)
	}
}

// For /reminder add, data.Name is the group and data.Options[0] is the
// subcommand, whose own .Options are the arguments — one level deeper than a
// top-level command.
func resolveApplicationCommand(data discordgo.ApplicationCommandInteractionData) (command.Command, []*discordgo.ApplicationCommandInteractionDataOption, bool) {
	if cmd, found := commandRegistry.Lookup(data.Name); found {
		return cmd, data.Options, true
	}

	if !isCommandGroup(data.Name) {
		// A stale command left registered at Discord's end lands here.
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

	// ResolveChat, so the slash and chat paths cannot reach different handlers.
	cmd, _, found := commandRegistry.ResolveChat(data.Name, []string{sub.Name})
	if !found {
		log.Z.Warn("unknown subcommand.",
			zap.String("command", data.Name), zap.String("subcommand", sub.Name))
		return command.Command{}, nil, false
	}

	return cmd, sub.Options, true
}

func isCommandGroup(name string) bool {
	for _, group := range commandRegistry.Groups() {
		if strings.EqualFold(group, name) {
			return true
		}
	}

	return false
}

// A Slow command is acknowledged before its handler runs: Discord kills an
// interaction that is not answered within three seconds.
func runInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, cmd command.Command, inv *command.Invocation, clients *client.Clients) {
	ctx, err := interactionContext(i, clients)
	if err != nil {
		respondError(s, i, err)
		return
	}

	if cmd.Slow {
		if !deferInteraction(s, i) {
			// Nothing can be delivered against an unacknowledged interaction,
			// so running the handler would apply a change it cannot report.
			return
		}

		resp, handlerErr := cmd.Handler(ctx, inv)
		if handlerErr != nil {
			log.Z.Error("command failed.", zap.String("command", cmd.Name), zap.Error(handlerErr))
			respondDeferredError(s, i, handlerErr)
			return
		}

		respondDeferred(s, i, resp)

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

// messageContentRequired reports whether the privileged MESSAGE_CONTENT intent
// is needed. WANHA's own need for it is applied at the call site in discord.go.
func messageContentRequired(prefixes []string, messageContent bool) bool {
	return len(prefixes) > 0 || messageContent
}

func triggerCandidate(content string, prefixes []string) bool {
	// HasPrefix rather than ParseChat: ParseChat reports no match for a bare
	// "??", which is ordinary chat shorthand and must not fire a trigger.
	if command.HasPrefix(content, prefixes) {
		return false
	}

	// The server refuses an empty phrase with InvalidArgument, so an
	// attachment-only or sticker-only message must not cost a round trip.
	return strings.TrimSpace(content) != ""
}

// mentionsBot reports whether a message DELIBERATELY mentions the bot. Matching
// by id defeats impostors, and requiring the <@id> token in the text as well as
// in Mentions excludes Discord's automatic reply ping.
func mentionsBot(s *discordgo.Session, m *discordgo.MessageCreate) bool {
	if s == nil || s.State == nil || s.State.User == nil {
		return false
	}

	self := s.State.User.ID

	mentioned := false
	for _, user := range m.Mentions {
		if user != nil && user.ID == self {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return false
	}

	// Older Discord clients wrote a nickname mention as <@!id>.
	return strings.Contains(m.Content, "<@"+self+">") ||
		strings.Contains(m.Content, "<@!"+self+">")
}

// triggerAttemptTimeout is the outer budget for one attempt; triggerFileTimeout
// must fit inside it to mean anything.
const triggerAttemptTimeout = 15 * time.Second

// A fired file trigger buffers the whole blob for Discord's upload, and
// discordgo dispatches every MessageCreate on its own goroutine, so without a
// cap concurrent buffers are bounded only by inbound message rate.
const maxConcurrentTriggerAttempts = 4

// Acquisition is non-blocking: a queued stale attempt would post to a
// conversation that has already moved on.
var triggerAttemptSlots = make(chan struct{}, maxConcurrentTriggerAttempts)

// Commands and triggers are exclusive: a prefixed message is a command even if
// it does not resolve.
func handleMessage(s *discordgo.Session, m *discordgo.MessageCreate, clients *client.Clients) {
	if !isHuman(s, m) {
		return
	}

	// Unconditional, before the prefix branch: a prefixed or attachment-only
	// message can still carry a repost. Its own goroutine because work follows
	// it here, and a CDN fetch would delay every command and trigger behind it.
	if config.Options.Repost.Enabled {
		go attemptRepost(s, m.Message, false, clients)
	}

	name, raw, prefixed := command.ParseChat(m.Content, config.Options.Discord.CommandPrefixes.Prefixes)
	if prefixed {
		dispatchChatCommand(s, m, name, raw, clients)
		return
	}

	if !triggerCandidate(m.Content, config.Options.Discord.CommandPrefixes.Prefixes) {
		return
	}

	// A mention is an explicit ask, so it bypasses the chance roll server-side.
	forced := mentionsBot(s, m)

	// Inline: discordgo already dispatches each handler on its own goroutine,
	// so this cannot stall the gateway receive loop.
	attemptTrigger(s, m, forced, clients)
}

// An edit matches but never inserts, so it cannot make itself match; CheckRepost
// enforces that server-side via edit=true.
func handleMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate, clients *client.Clients) {
	if !config.Options.Repost.Enabled {
		return
	}
	if !isHumanMessage(s, m.Message) {
		return
	}

	attemptRepost(s, m.Message, true, clients)
}

func dispatchChatCommand(s *discordgo.Session, m *discordgo.MessageCreate, name string, raw []string, clients *client.Clients) {
	cmd, args, found := commandRegistry.ResolveChat(name, raw)
	if !found {
		// Silent: other bots share the prefix, so answering every typo is noise.
		return
	}

	inv, err := command.Bind(cmd, args)
	if err != nil {
		respondChatError(s, m, err)
		return
	}

	resp, err := cmd.Handler(messageContext(m, clients), inv)
	if err != nil {
		log.Z.Error("chat command failed.", zap.String("command", cmd.Name), zap.Error(err))
		respondChatError(s, m, err)
		return
	}

	respondChat(s, m, resp)
}

// A non-match, a rate-limited forced fire, a DM and a failed RPC all say
// nothing; the alternative is a bot that comments on ordinary conversation.
func attemptTrigger(s *discordgo.Session, m *discordgo.MessageCreate, forced bool, clients *client.Clients) {
	select {
	case triggerAttemptSlots <- struct{}{}:
		defer func() { <-triggerAttemptSlots }()
	default:
		// Debug, not warn: under a burst this is the design working.
		log.Z.Debug("dropped a trigger attempt; too many already in flight.")
		return
	}

	ctx, cancel := context.WithTimeout(messageContext(m, clients), triggerAttemptTimeout)
	defer cancel()

	instance, err := currentTriggerInstance(ctx)
	if err != nil {
		// No instance means a direct message.
		return
	}

	// Raw content: matching and spoiler stripping are the server's (ADR-0019).
	phrase := m.Content

	// No caller identity in the request; it travels as a header. The forced-fire
	// limiter is server-side, where a second client cannot bypass it.
	req := pb.TryTriggerReq_builder{
		Instance: instance,
		Phrase:   &phrase,
		Forced:   &forced,
	}.Build()

	resp, err := clientsFrom(ctx).Trigger.TryTrigger(ctx, connect.NewRequest(req))
	if err != nil {
		// No phrase in the log line: neither stored phrases nor the message
		// matched against them may be echoed into logs.
		log.Z.Error("failed to call TryTrigger.", zap.Error(err))
		return
	}
	if resp.Msg.GetId() == "" {
		return
	}

	out, err := triggerPlaybackResponse(ctx, resp.Msg)
	if err != nil {
		log.Z.Error("failed to render a fired trigger.",
			zap.String("trigger_id", resp.Msg.GetId()), zap.Error(err))
		return
	}
	if out == nil {
		return
	}

	respondChat(s, m, out)
}

func isHuman(s *discordgo.Session, m *discordgo.MessageCreate) bool {
	return isHumanMessage(s, m.Message)
}

// m may be nil or carry a nil Author: Discord sends partial payloads for some
// update events, such as an embed-only update from a link unfurling.
func isHumanMessage(s *discordgo.Session, m *discordgo.Message) bool {
	if m == nil || m.Author == nil || m.Author.Bot {
		return false
	}

	if s.State != nil && s.State.User != nil && m.Author.ID == s.State.User.ID {
		return false
	}

	return true
}

func invocationFromOptions(cmd command.Command, options []*discordgo.ApplicationCommandInteractionDataOption) (*command.Invocation, error) {
	args := make(map[string]any, len(options))

	for _, option := range options {
		// Each accessor panics on a mismatched option type, and discordgo does
		// not recover, so every arm is matched explicitly with no fallthrough.
		switch option.Type {
		case discordgo.ApplicationCommandOptionInteger:
			args[option.Name] = option.IntValue()
		case discordgo.ApplicationCommandOptionBoolean:
			args[option.Name] = option.BoolValue()
		case discordgo.ApplicationCommandOptionString:
			args[option.Name] = option.StringValue()
		default:
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("unsupported option type %v for %q", option.Type, option.Name))
		}
	}

	return command.BindNamed(cmd, args)
}
