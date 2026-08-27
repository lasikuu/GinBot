package discord

import (
	"bytes"
	"errors"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// errorMessage maps a failed command onto a message for the caller.
//
// InvalidArgument and FailedPrecondition carry a message meant for the caller;
// anything else is internal and gets a generic reply so implementation detail
// does not leak into a channel.
//
// Both invocation paths share this mapping: a slash command and a chat command
// must not explain the same failure differently.
//
// connect.CodeOf(nil) reports CodeUnknown, which falls through to the generic
// message below like any other unrecognised code — so errorMessage(nil), which
// respondDeferred calls deliberately, still answers sensibly.
func errorMessage(err error) string {
	message := "Something went wrong."

	// A *connect.Error's own Error() string is prefixed with its code (e.g.
	// "invalid_argument: ..."), so the two message-bearing cases below read
	// connectErr.Message() rather than err.Error() — showing the caller the
	// prefix would leak wire-protocol vocabulary into a chat reply.
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition:
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			message = connectErr.Message()
		}
	case connect.CodePermissionDenied:
		message = "You are not allowed to do that."
	case connect.CodeNotFound:
		message = "Not found."
	case connect.CodeUnimplemented:
		message = "That is not implemented yet."
	case connect.CodeUnavailable:
		// Distinct from the generic message on purpose, and the distinction is
		// the whole point of the case: a backend the bot cannot reach is the
		// one failure here that is both transient and the caller's to act on,
		// so telling them to retry is actionable where "something went wrong"
		// invites a bug report about a working command. It leaks no
		// implementation detail — that ginbot-server exists and can be down is
		// not a secret the chat reply is keeping.
		message = "The bot's backend is unreachable right now. Try again in a moment."
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

// fallbackAttachmentName names an attachment whose own name is blank. Discord
// rejects a file with an empty filename, which would fail the whole send rather
// than just the attachment.
const fallbackAttachmentName = "attachment"

// responseFiles builds the discordgo attachments for a response.
//
// No size cap is applied here. storage.MaxFileBytes is enforced server-side on
// both write and read, and it is set to Discord's own 8 MiB baseline, so a
// second cap in this direction could only ever refuse a file Discord would have
// accepted.
//
// A FRESH reader is built on every call, deliberately: a reader is consumed by
// the send, so a shared one would put an empty file on any second use.
func responseFiles(resp *command.Response) []*discordgo.File {
	if resp == nil || resp.File == nil || len(resp.File.Content) == 0 {
		return nil
	}

	name := resp.File.Name
	if name == "" {
		name = fallbackAttachmentName
	}

	return []*discordgo.File{{
		Name:        name,
		ContentType: resp.File.MIMEType,
		Reader:      bytes.NewReader(resp.File.Content),
	}}
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
	// files is what that outgoing message attaches.
	files []*discordgo.File
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
//
// An attachment rides on EVERY branch, unlike the re-roll button. A file is the
// response's substance rather than a control on it, so a trigger that replies
// with an image has to arrive whether it was played back by a slash command, a
// chat command or a re-roll.
func planResponse(source commandSource, resp *command.Response) responsePlan {
	switch source {
	case sourceReRoll:
		return responsePlan{
			interactionResponse: discordgo.InteractionResponseDeferredMessageUpdate,
			replyInChannel:      true,
			files:               responseFiles(resp),
		}
	case sourceChat:
		return responsePlan{
			interactionResponse: interactionNone,
			replyInChannel:      true,
			components:          reRollComponents(resp),
			files:               responseFiles(resp),
		}
	}

	// A slash command is the only path where the callback carries the content.
	return responsePlan{
		interactionResponse: discordgo.InteractionResponseChannelMessageWithSource,
		components:          reRollComponents(resp),
		files:               responseFiles(resp),
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
			Files:           plan.files,
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
		Files:           plan.files,
		Reference:       reference,
		AllowedMentions: noMentions(),
	})
	if err != nil {
		// The interaction is already acknowledged, so the user sees no failure
		// banner and this log is the only trace.
		log.Z.Error("failed to send re-roll reply.", zap.Error(err))
	}
}

// deferInteraction acknowledges a slow command before its handler runs, and
// reports whether the acknowledgement landed.
//
// Discord invalidates an interaction after three seconds, and a command marked
// Slow can outlast that — /trigger add with a file makes the SERVER fetch from a
// CDN, bounded at 30 seconds. Without this the user sees "the application did
// not respond" while the trigger is in fact created, and the token is dead so
// nothing can report the outcome afterwards. Deferring buys a fifteen-minute
// follow-up window.
//
// Ephemeral, always. The acknowledgement has to commit to a visibility before
// the handler has produced a Response to read one from, and an ephemeral
// deferral cannot later be edited into a public message. Every Slow command
// today answers ephemerally anyway; a future public one has to post its own
// channel message the way the re-roll path does.
func deferInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		log.Z.Error("failed to defer a slow command.", zap.Error(err))
		return false
	}

	return true
}

// respondDeferred delivers a slow command's result into the placeholder
// deferInteraction left behind.
//
// It edits rather than sends: the deferral already created the message, so a
// follow-up would leave the "thinking" placeholder sitting in the channel
// forever. Content is never left empty — Discord rejects an edit with neither
// text nor an attachment, and an attachment-only response is legitimate here.
func respondDeferred(s *discordgo.Session, i *discordgo.InteractionCreate, resp *command.Response) {
	if resp == nil {
		log.Z.Error("slow command returned no response.", zap.String("command", i.Type.String()))
		resp = &command.Response{Content: errorMessage(nil)}
	}

	files := responseFiles(resp)

	content := truncateContent(resp.Content)
	if content == "" && len(files) == 0 {
		content = "Done."
	}

	_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:         &content,
		Files:           files,
		AllowedMentions: noMentions(),
	})
	if err != nil {
		// The interaction is already acknowledged, so the user sees the
		// placeholder rather than a failure banner and this log is the only
		// trace.
		log.Z.Error("failed to deliver a slow command's response.", zap.Error(err))
	}
}

// respondDeferredError reports a failed slow command into its placeholder.
func respondDeferredError(s *discordgo.Session, i *discordgo.InteractionCreate, err error) {
	content := errorMessage(err)

	if _, editErr := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:         &content,
		AllowedMentions: noMentions(),
	}); editErr != nil {
		log.Z.Error("failed to report a slow command's failure.", zap.Error(editErr))
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
		Files:           plan.files,
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
