package discord

import (
	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"strings"
)

var EntertainmentCommands = []*discordgo.ApplicationCommand{
	{
		Name:        "doubles",
		Description: "Roll for doubles",
		NameLocalizations: &map[discordgo.Locale]string{
			discordgo.Finnish: "tuplat",
		},
	},
	{
		Name:        "triples",
		Description: "Roll for triples",
		NameLocalizations: &map[discordgo.Locale]string{
			discordgo.Finnish: "triplat",
		},
	},
	{
		Name:        "number",
		Description: "Roll a random number between an interval",
		Options:     NumberOptions,
	},
}

var NumberOptions = []*discordgo.ApplicationCommandOption{
	{
		Name:        "lower",
		Description: "Lower bound, defaults to 0",
		Type:        discordgo.ApplicationCommandOptionInteger,
		Required:    false,
	},
	{
		Name:        "upper",
		Description: "Upper bound, defaults to 9",
		Type:        discordgo.ApplicationCommandOptionInteger,
		Required:    false,
	},
}

// Returns a button component with a die emoji.
// The customID string is used to connect the button's interaction to a handler.
// Used with doubles.
func createReRollButton(customID string) *discordgo.ActionsRow {
	var comp = discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.Button{
				CustomID: customID,
				Label:    "",
				Style:    discordgo.PrimaryButton,
				Emoji: &discordgo.ComponentEmoji{
					Name: "\U0001F3B2",
				},
				Disabled: false,
			},
		},
	}

	return &comp
}

func doublesPlusN(i *discordgo.InteractionCreate, digits int32) (string, error) {
	ctx, err := interactionContext(i)
	if err != nil {
		log.Z.Error("failed to get context", zap.Error(err))
		return "", err
	}

	reqType := pb.GetRandomNumberReq_DOUBLES

	req := pb.GetRandomNumberReq{
		Type:   &reqType,
		Digits: &digits,
	}

	resp, err := EntertainmentServiceClient.GetRandomNumber(ctx, &req)
	if err != nil {
		log.Z.Error("failed to call GetRandomNumber", zap.Error(err))
		return "", err
	}

	msg := resp.GetNumber()

	// Hits are bolded
	if strings.Count(msg, string(msg[0])) == len(msg) {
		msg = "**" + msg + "**"
	}

	return msg, nil
}

func boundedNumber(i *discordgo.InteractionCreate) (string, error) {
	ctx, err := interactionContext(i)
	if err != nil {
		log.Z.Error("failed to get context", zap.Error(err))
		return "", err
	}

	reqType := pb.GetRandomNumberReq_INTERVAL

	var lower int64 = 0
	var upper int64 = 10
	options := i.ApplicationCommandData().Options

	for i := range options {
		if options[i].Name == "lower" {
			lower = options[i].IntValue()
		}
		if options[i].Name == "upper" {
			upper = options[i].IntValue()
		}
	}

	req := pb.GetRandomNumberReq{
		Type:  &reqType,
		Lower: &lower,
		Upper: &upper,
	}

	resp, err := EntertainmentServiceClient.GetRandomNumber(ctx, &req)
	if err != nil {
		log.Z.Error("failed to call GetRandomNumber", zap.Error(err))
		return "", err
	}

	msg := "**" + resp.GetNumber() + "** \U0001F3B2"

	return msg, nil
}

func Doubles(s *discordgo.Session, i *discordgo.InteractionCreate) {
	msg, err := doublesPlusN(i, 2)
	if err != nil {
		log.Z.Error("failed to call doublesPlusN", zap.Error(err))
		return
	}

	var component []discordgo.MessageComponent
	if i.Type == discordgo.InteractionApplicationCommand {
		component = []discordgo.MessageComponent{
			createReRollButton("reRollDoubles"),
		}
	}

	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    msg,
			Components: component,
		},
	})
	if err != nil {
		log.Z.Error("failed to respond to Doubles", zap.Error(err))
	}
}

func Triples(s *discordgo.Session, i *discordgo.InteractionCreate) {
	msg, err := doublesPlusN(i, 3)
	if err != nil {
		log.Z.Error("failed to call doublesPlusN", zap.Error(err))
		return
	}

	var component []discordgo.MessageComponent
	if i.Type == discordgo.InteractionApplicationCommand {
		component = []discordgo.MessageComponent{
			createReRollButton("reRollTriples"),
		}
	}

	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    msg,
			Components: component,
		},
	})
	if err != nil {
		log.Z.Error("failed to respond to Doubles", zap.Error(err))
	}
}

func Number(s *discordgo.Session, i *discordgo.InteractionCreate) {
	msg, err := boundedNumber(i)
	if err != nil {
		log.Z.Error("failed to call boundedNumber", zap.Error(err))
		return
	}

	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
		},
	})
	if err != nil {
		log.Z.Error("failed to respond to Number", zap.Error(err))
	}
}
