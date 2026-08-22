package discord

import (
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// respondError answers an interaction with an error message.
//
// Every interaction must be answered within three seconds or Discord shows
// "the application did not respond". Bailing out after only logging leaves the
// user staring at that, so a failed command reports why instead.
//
// InvalidArgument and FailedPrecondition carry a message meant for the caller;
// anything else is internal and gets a generic reply so implementation detail
// does not leak into a channel.
func respondError(s *discordgo.Session, i *discordgo.InteractionCreate, err error) {
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

	respondErr := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: message,
			// Only the invoking user sees the error.
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
	if respondErr != nil {
		log.Z.Error("failed to send error response", zap.Error(respondErr))
	}
}
