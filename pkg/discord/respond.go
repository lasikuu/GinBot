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

// respondCommand renders a command response to an interaction.
//
// A re-roll edits the message it was clicked from rather than posting a new
// one. The button survives the edit, so re-rolling repeatedly leaves one
// message that keeps changing instead of a growing chain of them.
func respondCommand(s *discordgo.Session, i *discordgo.InteractionCreate, resp *command.Response) {
	if resp == nil {
		log.Z.Error("command returned no response.", zap.String("command", i.Type.String()))
		return
	}

	data := &discordgo.InteractionResponseData{
		Content:         truncateContent(resp.Content),
		Components:      reRollComponents(resp),
		AllowedMentions: noMentions(),
	}
	if resp.Ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}

	responseType := discordgo.InteractionResponseChannelMessageWithSource
	if i.Type == discordgo.InteractionMessageComponent {
		responseType = discordgo.InteractionResponseUpdateMessage
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: responseType,
		Data: data,
	})
	if err != nil {
		log.Z.Error("failed to respond to command.", zap.Error(err))
	}
}

// respondChat renders a command response to a chat message.
//
// Ephemeral has no chat equivalent and is ignored, as Response documents.
func respondChat(s *discordgo.Session, m *discordgo.MessageCreate, resp *command.Response) {
	if resp == nil {
		log.Z.Error("chat command returned no response.")
		return
	}

	_, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content:         truncateContent(resp.Content),
		Components:      reRollComponents(resp),
		Reference:       m.Reference(),
		AllowedMentions: noMentions(),
	})
	if err != nil {
		log.Z.Error("failed to respond to chat command.", zap.Error(err))
	}
}

// reRollComponents attaches the re-roll button whenever the response asks for
// one. It is attached to re-rolled messages too: previously the button was
// added only for the initial slash command, so a re-rolled message lost it.
//
// Returning an empty slice rather than nil matters on the update path: nil
// leaves the existing components in place, so a response that no longer wants a
// button could not remove one.
func reRollComponents(resp *command.Response) []discordgo.MessageComponent {
	if resp.ReRollID == "" {
		return []discordgo.MessageComponent{}
	}

	return []discordgo.MessageComponent{
		createReRollButton(resp.ReRollID),
	}
}
