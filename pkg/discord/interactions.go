package discord

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
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
//
// A command declaring Slow is acknowledged BEFORE its handler runs, because
// Discord kills the interaction after three seconds and the handler may take
// longer than that. Everything else answers in the callback, which keeps the
// response and the acknowledgement to a single round trip.
func runInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, cmd command.Command, inv *command.Invocation) {
	ctx, err := interactionContext(i)
	if err != nil {
		respondError(s, i, err)
		return
	}

	if cmd.Slow {
		if !deferInteraction(s, i) {
			// Nothing can be delivered against an interaction that was never
			// acknowledged, and running the handler anyway would apply the
			// change with no way to report it.
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

// messageContentRequired reports whether the bot must request the privileged
// MESSAGE_CONTENT intent.
//
// Either capability alone justifies it: a chat command needs the content to find
// its prefix, and trigger matching needs the content to match a phrase against
// at all. They are separate switches because a deployment can want one without
// the other.
//
// WANHA needing the same intent is deliberately NOT folded in here: this
// function's signature is exercised directly by TestMessageContentRequired,
// and widening it would break that test for a change this package's own
// call site can express just as well with a plain ||. See discord.go.
func messageContentRequired(prefixes []string, messageContent bool) bool {
	return len(prefixes) > 0 || messageContent
}

// triggerCandidate reports whether a message should be offered to TryTrigger.
func triggerCandidate(content string, prefixes []string) bool {
	// A prefix is an explicit address to a bot — this one or another one sharing
	// the prefix. Firing a random trigger on a mistyped or somebody else's
	// command would be surprising, so a prefixed message is never a candidate,
	// whether or not a command name follows the prefix or resolves here.
	// command.HasPrefix rather than ParseChat: ParseChat reports no match for a
	// bare "??", which is ordinary chat shorthand and must not fire a trigger.
	if command.HasPrefix(content, prefixes) {
		return false
	}

	// The server refuses an empty phrase with InvalidArgument, so an
	// attachment-only or sticker-only message must not cost a round trip.
	return strings.TrimSpace(content) != ""
}

// mentionsBot reports whether a message DELIBERATELY mentions the bot.
//
// The comparison is by id, never by display name: a name is attacker-controlled
// and a nickname is per-guild, so matching one would both miss real mentions and
// fire on impostors.
//
// The mention has to appear in the message TEXT as well as in Mentions, which is
// what makes it deliberate. Discord adds the replied-to author to Mentions
// whenever mention_author is set, and that is the client default — so without
// this, clicking Reply on any message the bot posted and typing anything at all
// would force a fire. An explicit mention always renders as the <@id> token in
// the content; a reply ping never does.
//
// Every pointer is guarded. discordgo leaves State unpopulated until the session
// is open and dispatches handlers as bare goroutines with no recover(), so a nil
// deref here would take the process down.
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

	// Both forms: Discord's older clients wrote a nickname mention as <@!id>,
	// and messages carrying one are still in every channel's history.
	return strings.Contains(m.Content, "<@"+self+">") ||
		strings.Contains(m.Content, "<@!"+self+">")
}

// triggerAttemptTimeout bounds one whole trigger attempt: the TryTrigger call
// plus, when something fires, the GetFile that pulls its bytes back. It is the
// outer budget, so triggerFileTimeout has to fit inside it to mean anything.
const triggerAttemptTimeout = 15 * time.Second

// maxConcurrentTriggerAttempts caps how many trigger attempts are in flight at
// once.
//
// A fired file trigger buffers the whole blob in memory, up to
// storage.MaxFileBytes, and ADR-0022 records that this happens twice over during
// unmarshalling with no backpressure anywhere. discordgo dispatches every
// MessageCreate on its own goroutine, so without a cap the number of concurrent
// buffers is set by nothing but inbound message rate — an out-of-memory kill
// reachable by ordinary chat traffic in a busy guild.
const maxConcurrentTriggerAttempts = 4

// triggerAttemptSlots is that cap. Acquisition is non-blocking: dropping a
// trigger under load is the right failure, because a queue of stale attempts
// would post auto-responders to conversations that have already moved on.
var triggerAttemptSlots = make(chan struct{}, maxConcurrentTriggerAttempts)

// handleMessage routes a chat message: first through the command registry when
// it carries a prefix, otherwise through trigger matching.
//
// The two are exclusive. A prefixed message is a command, and a command that
// does not resolve is still not an invitation to fire an auto-responder.
func handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if !isHuman(s, m) {
		return
	}

	// Checked before the prefix branch, and unconditionally on every human
	// message rather than only unprefixed ones: triggerCandidate deliberately
	// skips attachment-only messages and a prefixed command message, but
	// WANHA must not be gated by either — a prefixed message can still carry
	// a link, and an attachment-only message is exactly what WANHA exists to
	// catch.
	//
	// Its own goroutine, unlike attemptTrigger. A repost check costs a CDN
	// fetch plus a decode server-side, bounded only by repostAttemptTimeout,
	// and it is NOT the last thing this handler does — running it inline would
	// delay every chat command and every trigger behind it by up to that
	// timeout, a regression in paths that worked before this feature existed.
	// attemptTrigger's reasoning for staying inline does not transfer, because
	// nothing follows it. Concurrency and memory are already bounded
	// independently by repostAttemptSlots.
	if config.Options.Repost.Enabled {
		go attemptRepost(s, m.Message, false)
	}

	name, raw, prefixed := command.ParseChat(m.Content, config.Options.Discord.CommandPrefixes.Prefixes)
	if prefixed {
		dispatchChatCommand(s, m, name, raw)
		return
	}

	if !triggerCandidate(m.Content, config.Options.Discord.CommandPrefixes.Prefixes) {
		return
	}

	// A mention is an explicit ask, so it bypasses the chance roll server-side.
	forced := mentionsBot(s, m)

	// No goroutine here on purpose. discordgo already dispatches every event
	// handler on its own goroutine (Session.handle, unless SyncEvents is set,
	// which nothing here sets), so this handler is not the gateway receive loop
	// and cannot stall it. Spawning another goroutine would only make the
	// attempt unobservable to the caller while bounding nothing — what actually
	// needs bounding is memory and RPC lifetime, which is what
	// triggerAttemptSlots and triggerAttemptTimeout do.
	attemptTrigger(s, m, forced)
}

// handleMessageUpdate offers an edited message to WANHA. An edit MATCHES but
// never INSERTS (W8): CheckRepost enforces that server-side via edit=true,
// this is just the client-side trigger for it — editing a message to add a
// link should be able to trigger WANHA, but must not seed the index and make
// the edited message match itself.
func handleMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if !config.Options.Repost.Enabled {
		return
	}
	if !isHumanMessage(s, m.Message) {
		return
	}

	attemptRepost(s, m.Message, true)
}

// dispatchChatCommand runs a prefixed chat message through the registry.
func dispatchChatCommand(s *discordgo.Session, m *discordgo.MessageCreate, name string, raw []string) {
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

// attemptTrigger offers one message to the server's matching engine and posts
// whatever fires.
//
// It shares commandContext with the command path so identity and origin travel
// as metadata rather than as request fields, and it renders through respondChat
// so truncation, mention suppression and attachment rendering are the same code
// a command reply goes through.
//
// A non-match says NOTHING. Neither does a rate-limited forced fire, a direct
// message with no instance, or a failed RPC: the alternative is a bot that
// comments on ordinary conversation.
func attemptTrigger(s *discordgo.Session, m *discordgo.MessageCreate, forced bool) {
	select {
	case triggerAttemptSlots <- struct{}{}:
		defer func() { <-triggerAttemptSlots }()
	default:
		// Debug, not warn: under a burst this is the design working, and a log
		// line per dropped message would be its own flood.
		log.Z.Debug("dropped a trigger attempt; too many already in flight.")
		return
	}

	ctx, cancel := context.WithTimeout(messageContext(m), triggerAttemptTimeout)
	defer cancel()

	instance, err := currentTriggerInstance(ctx)
	if err != nil {
		// No instance means a direct message. Not worth a log line per message.
		return
	}

	// The raw content, not a trimmed or lowered copy: the matching modes and the
	// spoiler stripping are the server's to define (ADR-0019).
	phrase := m.Content

	// No user_id: the server reads the caller from metadata and rejects any
	// other. No client-side forced-fire limiter either — trigger.ForcedLimiter
	// already bounds it to once per author per interval, server-side, where it
	// cannot be bypassed by a second client.
	req := pb.TryTriggerReq_builder{
		Instance: instance,
		Phrase:   &phrase,
		Forced:   &forced,
	}.Build()

	resp, err := client.TriggerServiceClient.TryTrigger(ctx, req)
	if err != nil {
		// No phrase in the log line: a stored phrase must not be echoed into
		// logs, and neither must the message that was matched against it.
		log.Z.Error("failed to call TryTrigger.", zap.Error(err))
		return
	}
	if resp.GetId() == "" {
		return
	}

	out, err := triggerPlaybackResponse(ctx, resp)
	if err != nil {
		log.Z.Error("failed to render a fired trigger.",
			zap.String("trigger_id", resp.GetId()), zap.Error(err))
		return
	}
	if out == nil {
		return
	}

	respondChat(s, m, out)
}

// isHuman reports whether a message should be considered for dispatch.
// Delegates to isHumanMessage; kept with this exact signature because
// existing tests call it directly.
func isHuman(s *discordgo.Session, m *discordgo.MessageCreate) bool {
	return isHumanMessage(s, m.Message)
}

// isHumanMessage is isHuman's logic, lifted to work on a bare *discordgo.Message
// so handleMessageUpdate can reuse it: MessageUpdate wraps the same *Message
// shape as MessageCreate, but is not a MessageCreate itself. Ignoring bots
// covers this bot's own messages as well; the explicit self check guards
// against a future non-bot token and makes the intent obvious.
//
// m may be nil, or carry a nil Author: Discord sends partial payloads for
// some message update events (an embed-only update from a link unfurling,
// for instance), and neither must reach a nil dereference here.
func isHumanMessage(s *discordgo.Session, m *discordgo.Message) bool {
	if m == nil || m.Author == nil || m.Author.Bot {
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
