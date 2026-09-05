package discord

import (
	"bytes"
	"errors"
	"strings"
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

// splitContent chunks content at no more than limit bytes, preferring a
// newline boundary; a single line longer than limit is cut on a rune boundary
// so every chunk stays valid UTF-8. See ADR-0040.
func splitContent(content string, limit int) []string {
	if content == "" {
		return nil
	}
	if len(content) <= limit {
		return []string{content}
	}

	var chunks []string
	var current strings.Builder

	for _, line := range strings.Split(content, "\n") {
		joined := current.Len()
		if joined > 0 {
			joined++ // the "\n" a further line would need
		}
		joined += len(line)

		if joined <= limit {
			if current.Len() > 0 {
				current.WriteByte('\n')
			}
			current.WriteString(line)
			continue
		}

		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}

		remaining := line
		for len(remaining) > limit {
			cut := limit
			for cut > 0 && !utf8.RuneStart(remaining[cut]) {
				cut--
			}
			// Only when limit is below the 4 bytes a rune can need, where no
			// cut is on a boundary and something must still be emitted.
			if cut == 0 {
				cut = limit
			}
			chunks = append(chunks, remaining[:cut])
			remaining = remaining[cut:]
		}
		current.WriteString(remaining)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
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
	content             string
	// replyInChannel sends the content as its own channel message rather than
	// in the interaction callback.
	replyInChannel bool
	components     []discordgo.MessageComponent
	files          []*discordgo.File
}

// A re-roll reply is an ordinary channel message, so it carries none of the
// "used /doubles" attribution Discord puts above a slash response and is not a
// reply to the clicker: without the name beside the number, nothing says who
// rolled. A backtick in the name would escape the code span.
func attributeRoll(content string, invokerName string) string {
	if content == "" || invokerName == "" {
		return content
	}

	return content + " `" + strings.ReplaceAll(invokerName, "`", "") + "`"
}

// A re-roll is the odd one out: an interaction callback carries no
// message_reference, so it can never be a reply. The click is acknowledged and
// the new roll follows as a separate reply, without a button of its own.
func planResponse(source commandSource, resp *command.Response, invokerName string) responsePlan {
	switch source {
	case sourceReRoll:
		return responsePlan{
			interactionResponse: discordgo.InteractionResponseDeferredMessageUpdate,
			content:             attributeRoll(resp.Content, invokerName),
			replyInChannel:      true,
			files:               responseFiles(resp),
		}
	case sourceChat:
		return responsePlan{
			interactionResponse: interactionNone,
			content:             resp.Content,
			replyInChannel:      true,
			components:          reRollComponents(resp),
			files:               responseFiles(resp),
		}
	}

	return responsePlan{
		interactionResponse: discordgo.InteractionResponseChannelMessageWithSource,
		content:             resp.Content,
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
	invokerName := ""
	if i.Type == discordgo.InteractionMessageComponent {
		source = sourceReRoll
		if user := interactionUser(i); user != nil {
			invokerName = user.Username
		}
	}
	plan := planResponse(source, resp, invokerName)

	response := &discordgo.InteractionResponse{Type: plan.interactionResponse}
	if !plan.replyInChannel {
		response.Data = &discordgo.InteractionResponseData{
			Content:         truncateContent(plan.content),
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
		Content:         truncateContent(plan.content),
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
// window. Visibility is decided by the command's declared Ephemeral, read
// before the handler runs, since an ephemeral deferral cannot later be edited
// into a public message. See ADR-0038.
func deferInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, ephemeral bool) bool {
	data := &discordgo.InteractionResponseData{}
	if ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: data,
	})
	if err != nil {
		log.Z.Error("failed to defer a slow command.", zap.Error(err))
		return false
	}

	return true
}

// Edits rather than sends, or the "thinking" placeholder stays forever.
// A follow-up does not inherit the deferral's visibility, so it is set again.
func respondDeferred(s *discordgo.Session, i *discordgo.InteractionCreate, resp *command.Response, ephemeral bool) {
	if resp == nil {
		log.Z.Error("slow command returned no response.", zap.String("command", i.Type.String()))
		resp = &command.Response{Content: errorMessage(nil)}
	}

	// A deferral is acknowledged before the handler runs, so its visibility is
	// already fixed. A handler asking for private here gets public: log it, or
	// the next such branch leaks into the channel with no trace.
	if resp.Ephemeral && !ephemeral {
		log.Z.Error("a public command asked for an ephemeral deferred reply and was answered publicly.",
			zap.String("command", i.ApplicationCommandData().Name))
	}

	files := responseFiles(resp)
	chunks := splitContent(resp.Content, maxChatContent)

	first := ""
	if len(chunks) > 0 {
		first = chunks[0]
	}
	// Content is never empty: Discord rejects an edit with neither text nor an attachment.
	if first == "" && len(files) == 0 {
		first = "Done."
	}

	edit := &discordgo.WebhookEdit{
		Content:         &first,
		Files:           files,
		AllowedMentions: noMentions(),
	}
	if components := reRollComponents(resp); len(components) > 0 {
		edit.Components = &components
	}

	if _, err := s.InteractionResponseEdit(i.Interaction, edit); err != nil {
		// Already acknowledged, so this log is the only trace.
		log.Z.Error("failed to deliver a slow command's response.", zap.Error(err))
		return
	}

	// Guarded, not chunks[1:]: splitContent returns nil for empty content, and
	// a file reply legitimately has none.
	if len(chunks) < 2 {
		return
	}

	for _, chunk := range chunks[1:] {
		params := &discordgo.WebhookParams{
			Content:         chunk,
			AllowedMentions: noMentions(),
		}
		if ephemeral {
			params.Flags = discordgo.MessageFlagsEphemeral
		}

		if _, err := s.FollowupMessageCreate(i.Interaction, false, params); err != nil {
			// Already delivered in part, so this log is the only trace.
			log.Z.Error("failed to send a follow-up chunk of a slow command's response.", zap.Error(err))
			return
		}
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

// Ephemeral has no chat equivalent and is ignored. The posted message is
// returned for the undo window (ADR-0037), and is nil when nothing was sent.
func respondChat(s *discordgo.Session, m *discordgo.MessageCreate, resp *command.Response) *discordgo.Message {
	if resp == nil {
		log.Z.Error("chat command returned no response.")
		return nil
	}

	if resp.DirectWhenLong && len(resp.Content) > maxChatContent {
		return respondChatDirect(s, m, resp)
	}

	plan := planResponse(sourceChat, resp, "")

	sent, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content:         truncateContent(plan.content),
		Components:      plan.components,
		Files:           plan.files,
		Reference:       m.Reference(),
		AllowedMentions: noMentions(),
	})
	if err != nil {
		log.Z.Error("failed to respond to chat command.", zap.Error(err))
		return nil
	}

	return sent
}

// respondChatDirect delivers content too long for one channel message by DM,
// leaving a pointer in the channel. See ADR-0040.
func respondChatDirect(s *discordgo.Session, m *discordgo.MessageCreate, resp *command.Response) *discordgo.Message {
	// Response.File must not be silently dropped. No caller pairs a file with
	// DirectWhenLong today, so this reports rather than handles it.
	if resp.File != nil {
		log.Z.Error("a long chat response carried a file, which a DM delivery drops.",
			zap.String("filename", resp.File.Name))
	}

	if !sendDMChunks(s, m.Author.ID, splitContent(resp.Content, maxChatContent)) {
		sent, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
			Content:         "That's too long to post here, and I could not send it to you by DM. Check that you allow DMs from server members.",
			Reference:       m.Reference(),
			AllowedMentions: noMentions(),
		})
		if err != nil {
			log.Z.Error("failed to report a failed DM delivery.", zap.Error(err))
			return nil
		}
		return sent
	}

	sent, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content:         "Sent you a DM — it was too long to post here.",
		Reference:       m.Reference(),
		AllowedMentions: noMentions(),
	})
	if err != nil {
		log.Z.Error("failed to post the DM pointer.", zap.Error(err))
		return nil
	}

	return sent
}

// sendDMChunks opens a DM with userID and posts every chunk in order,
// stopping and reporting false on the first failure.
func sendDMChunks(s *discordgo.Session, userID string, chunks []string) bool {
	if len(chunks) == 0 {
		return false
	}

	channel, err := s.UserChannelCreate(userID)
	if err != nil {
		log.Z.Warn("failed to open a DM channel for a long response.", zap.String("user_id", userID), zap.Error(err))
		return false
	}

	for _, chunk := range chunks {
		if _, err := s.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
			Content:         chunk,
			AllowedMentions: noMentions(),
		}); err != nil {
			log.Z.Warn("failed to send a chunk of a long response by DM.", zap.String("user_id", userID), zap.Error(err))
			return false
		}
	}

	return true
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
