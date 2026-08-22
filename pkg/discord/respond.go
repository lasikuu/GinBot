package discord

import (
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errorMessage maps a failed command onto a message for the caller.
//
// InvalidArgument and FailedPrecondition carry a message meant for the caller;
// anything else is internal and gets a generic reply so implementation detail
// does not leak into a channel.
//
// Both invocation paths share this mapping: a slash command and a chat command
// must not explain the same failure differently.
func errorMessage(err error) string {
	message := "Something went wrong."

	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument, codes.FailedPrecondition:
			message = st.Message()
		case codes.PermissionDenied:
			message = "You are not allowed to do that."
		case codes.NotFound:
			message = "Not found."
		case codes.Unimplemented:
			message = "That is not implemented yet."
		}
	}

	return message
}

// respondError answers an interaction with an error message.
//
// Every interaction must be answered within three seconds or Discord shows
// "the application did not respond". Bailing out after only logging leaves the
// user staring at that, so a failed command reports why instead.
func respondError(s *discordgo.Session, i *discordgo.InteractionCreate, err error) {
	respondErr := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: errorMessage(err),
			// Only the invoking user sees the error.
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
	if respondErr != nil {
		log.Z.Error("failed to send error response", zap.Error(respondErr))
	}
}

// maxChatContent caps an outgoing chat message. Discord rejects a send above
// 2000 characters outright, which would turn a long echoed argument into no
// reply at all.
const maxChatContent = 2000

// noMentions stops Discord parsing any mention in an outgoing chat message.
//
// Error messages echo the caller's own argument back (`got "..."`), and a chat
// reply is a plain channel message, so without this a member can make the bot
// post @everyone or a role ping on their behalf: `??number @everyone`. An empty
// Parse list is not the same as omitting the field — omitting it means "parse
// everything", which is discordgo's default for ChannelMessageSend*.
//
// It also suppresses the reply ping, which would otherwise fire on every chat
// command because replied_user defaults to true.
func noMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{
		Parse: []discordgo.AllowedMentionType{},
	}
}

// truncateContent keeps a message inside Discord's limit, preferring a visibly
// cut message over a send that fails.
func truncateContent(content string) string {
	if len(content) <= maxChatContent {
		return content
	}

	// Cut on a rune boundary so the result is still valid UTF-8.
	const ellipsis = "…"
	cut := maxChatContent - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}

	return content[:cut] + ellipsis
}

// respondChatError answers a failed chat command. A chat message is not an
// interaction, so there is no ephemeral flag to hide the reply behind; it goes
// to the channel as a reply to the invoking message.
func respondChatError(s *discordgo.Session, m *discordgo.MessageCreate, err error) {
	_, respondErr := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content:         truncateContent(errorMessage(err)),
		Reference:       m.Reference(),
		AllowedMentions: noMentions(),
	})
	if respondErr != nil {
		log.Z.Error("failed to send chat error response", zap.Error(respondErr))
	}
}

// respondStale answers an interaction the bot can no longer service, such as a
// button on a message posted by an older version. Silence would show the user
// "the application did not respond" with no explanation.
func respondStale(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "That control is no longer available. Run the command again.",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		log.Z.Error("failed to send stale interaction response", zap.Error(err))
	}
}

// commandSource is how a command reached the bot.
type commandSource int

const (
	sourceSlash commandSource = iota
	sourceChat
	sourceReRoll
)

// interactionNone marks a plan with no interaction to answer. Discord numbers
// its callback types from 1, so zero is free to mean "not an interaction".
const interactionNone discordgo.InteractionResponseType = 0

// responsePlan is how one command result reaches the channel. Splitting the
// decision out from the calls it drives is what makes it verifiable: those
// calls need a discordgo.Session and there is no fake for one.
type responsePlan struct {
	// interactionResponse is the callback that answers the interaction, sent
	// before anything else. interactionNone for a chat command, which is not an
	// interaction.
	interactionResponse discordgo.InteractionResponseType
	// replyInChannel says the content goes out as its own channel message,
	// replying to the message that invoked it, rather than riding along in the
	// interaction callback.
	replyInChannel bool
	// components is what that outgoing message carries.
	components []discordgo.MessageComponent
}

// planResponse decides how a command result is delivered.
//
// A re-roll is the odd one out. Discord's interaction callback payload has no
// message_reference — discordgo.InteractionResponseData has no Reference field
// to put one in — so a callback can never itself be a reply. The click is
// therefore only acknowledged, which leaves the clicked message untouched and
// its button live, and the new roll follows as a separate reply to it. That
// reply carries no button of its own, so the chain stops after one hop and
// clicking the original again simply adds another reply to the same original.
func planResponse(source commandSource, resp *command.Response) responsePlan {
	switch source {
	case sourceReRoll:
		return responsePlan{
			interactionResponse: discordgo.InteractionResponseDeferredMessageUpdate,
			replyInChannel:      true,
		}
	case sourceChat:
		return responsePlan{
			interactionResponse: interactionNone,
			replyInChannel:      true,
			components:          reRollComponents(resp),
		}
	}

	// A slash command is the only path where the callback carries the content.
	return responsePlan{
		interactionResponse: discordgo.InteractionResponseChannelMessageWithSource,
		components:          reRollComponents(resp),
	}
}

// respondCommand renders a command response to an interaction.
func respondCommand(s *discordgo.Session, i *discordgo.InteractionCreate, resp *command.Response) {
	if resp == nil {
		log.Z.Error("command returned no response.", zap.String("command", i.Type.String()))
		return
	}

	source := sourceSlash
	if i.Type == discordgo.InteractionMessageComponent {
		source = sourceReRoll
	}
	plan := planResponse(source, resp)

	response := &discordgo.InteractionResponse{Type: plan.interactionResponse}
	if !plan.replyInChannel {
		response.Data = &discordgo.InteractionResponseData{
			Content:         truncateContent(resp.Content),
			Components:      plan.components,
			AllowedMentions: noMentions(),
		}
		if resp.Ephemeral {
			response.Data.Flags = discordgo.MessageFlagsEphemeral
		}
	}

	// Acknowledge before sending anything else. The callback has three seconds
	// or the user sees "This interaction failed", and the send below is a second
	// round trip that can outlast them.
	if err := s.InteractionRespond(i.Interaction, response); err != nil {
		log.Z.Error("failed to respond to command.", zap.Error(err))
	}

	if !plan.replyInChannel {
		return
	}

	reference := clickedMessageReference(i)
	if reference == nil {
		log.Z.Warn("component interaction carried no message.", zap.String("interaction", i.ID))
	}

	// Ephemeral is dropped here for the same reason it is on the chat path: a
	// plain channel message has no such flag.
	_, err := s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content:         truncateContent(resp.Content),
		Components:      plan.components,
		Reference:       reference,
		AllowedMentions: noMentions(),
	})
	if err != nil {
		// The interaction is already acknowledged, so the user sees no failure
		// banner and this log is the only trace.
		log.Z.Error("failed to send re-roll reply.", zap.Error(err))
	}
}

// clickedMessageReference points a re-roll reply at the message whose button
// was clicked.
//
// Interaction.Message is populated only for a component interaction, and
// discordgo dispatches handlers as bare goroutines with no recover(), so a nil
// deref here would take the whole process down. A nil reference still posts the
// roll, just not as a reply.
func clickedMessageReference(i *discordgo.InteractionCreate) *discordgo.MessageReference {
	if i.Message == nil {
		return nil
	}

	return i.Message.Reference()
}

// respondChat renders a command response to a chat message.
//
// Ephemeral has no chat equivalent and is ignored, as Response documents.
func respondChat(s *discordgo.Session, m *discordgo.MessageCreate, resp *command.Response) {
	if resp == nil {
		log.Z.Error("chat command returned no response.")
		return
	}

	plan := planResponse(sourceChat, resp)

	_, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content:         truncateContent(resp.Content),
		Components:      plan.components,
		Reference:       m.Reference(),
		AllowedMentions: noMentions(),
	})
	if err != nil {
		log.Z.Error("failed to respond to chat command.", zap.Error(err))
	}
}

// reRollComponents attaches the re-roll button whenever the response asks for
// one. Only a first roll reaches it: planResponse withholds the button from a
// re-roll so that the chain stops after one hop.
//
// nil is returned for "no button". It used to be an empty slice, because on the
// old in-place edit path nil left the existing components alone and so could
// not clear a button; no message is edited any more, and on a message create
// nil and an empty slice both mean no components.
func reRollComponents(resp *command.Response) []discordgo.MessageComponent {
	if resp.ReRollID == "" {
		return nil
	}

	return []discordgo.MessageComponent{
		createReRollButton(resp.ReRollID),
	}
}
