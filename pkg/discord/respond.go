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

// errorMessage maps a failed command onto a message for the caller. Only
// InvalidArgument and FailedPrecondition pass through verbatim; anything else
// is generic, so implementation detail cannot leak into a channel.
func errorMessage(err error) string {
	message := "Something went wrong."

	// Message() not Error(): the latter is prefixed with the wire code name.
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition:
		if connectErr, ok := errors.AsType[*connect.Error](err); ok {
			message = connectErr.Message()
		}
	case connect.CodePermissionDenied:
		message = "You are not allowed to do that."
	case connect.CodeNotFound:
		message = "Not found."
	case connect.CodeUnimplemented:
		message = "That is not implemented yet."
	case connect.CodeUnavailable:
		// The one failure that is transient and actionable by the caller.
		message = "The bot's backend is unreachable right now. Try again in a moment."
	}

	return message
}

// Every interaction must be answered within three seconds, or Discord shows
// "the application did not respond".
func respondError(s *discordgo.Session, i *discordgo.InteractionCreate, err error) {
	respondErr := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: errorMessage(err),
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
	if respondErr != nil {
		log.Z.Error("failed to send error response", zap.Error(respondErr))
	}
}

// Discord rejects a send above 2000 characters outright.
const maxChatContent = 2000

// noMentions stops Discord parsing mentions in an outgoing message: error
// replies echo the caller's argument, so `??number @everyone` would otherwise
// ping on their behalf. An empty Parse list is not the same as omitting it.
func noMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{
		Parse: []discordgo.AllowedMentionType{},
	}
}

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

// Discord rejects a file with an empty filename, failing the whole send.
const fallbackAttachmentName = "attachment"

// A fresh reader every call: the send consumes it, so a shared one would put an
// empty file on any second use.
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

// A chat message is not an interaction, so there is no ephemeral flag.
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

// Answers an interaction the bot can no longer service, such as a button on a
// message posted by an older version.
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

// Discord numbers its callback types from 1, so zero is free to mean "not an
// interaction".
const interactionNone discordgo.InteractionResponseType = 0

// responsePlan is how one command result reaches the channel.
type responsePlan struct {
	interactionResponse discordgo.InteractionResponseType
	// replyInChannel sends the content as its own channel message rather than
	// in the interaction callback.
	replyInChannel bool
	components     []discordgo.MessageComponent
	files          []*discordgo.File
}

// A re-roll is the odd one out: an interaction callback carries no
// message_reference, so it can never be a reply. The click is acknowledged and
// the new roll follows as a separate reply, without a button of its own.
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

	return responsePlan{
		interactionResponse: discordgo.InteractionResponseChannelMessageWithSource,
		components:          reRollComponents(resp),
		files:               responseFiles(resp),
	}
}

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

	// Acknowledge first: the callback has three seconds, and the send below is
	// a second round trip that can outlast them.
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

	// Ephemeral is dropped: a plain channel message has no such flag.
	_, err := s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content:         truncateContent(resp.Content),
		Components:      plan.components,
		Files:           plan.files,
		Reference:       reference,
		AllowedMentions: noMentions(),
	})
	if err != nil {
		// Already acknowledged, so this log is the only trace.
		log.Z.Error("failed to send re-roll reply.", zap.Error(err))
	}
}

// Deferring trades Discord's 3s interaction deadline for a 15-minute follow-up
// window. Always ephemeral: visibility must be chosen before the handler runs,
// and an ephemeral deferral cannot later be edited into a public message.
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

// Edits rather than sends, or the "thinking" placeholder stays forever. Content
// is never empty: Discord rejects an edit with neither text nor an attachment.
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
		// Already acknowledged, so this log is the only trace.
		log.Z.Error("failed to deliver a slow command's response.", zap.Error(err))
	}
}

func respondDeferredError(s *discordgo.Session, i *discordgo.InteractionCreate, err error) {
	content := errorMessage(err)

	if _, editErr := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:         &content,
		AllowedMentions: noMentions(),
	}); editErr != nil {
		log.Z.Error("failed to report a slow command's failure.", zap.Error(editErr))
	}
}

// Interaction.Message is populated only for a component interaction. A nil
// reference still posts the roll, just not as a reply.
func clickedMessageReference(i *discordgo.InteractionCreate) *discordgo.MessageReference {
	if i.Message == nil {
		return nil
	}

	return i.Message.Reference()
}

// Ephemeral has no chat equivalent and is ignored.
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

// Only a first roll reaches this: planResponse withholds the button from a
// re-roll so the chain stops after one hop.
func reRollComponents(resp *command.Response) []discordgo.MessageComponent {
	if resp.ReRollID == "" {
		return nil
	}

	return []discordgo.MessageComponent{
		createReRollButton(resp.ReRollID),
	}
}
