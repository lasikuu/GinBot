package discord

import (
	"context"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// digitRoll declares one member of the doubles family. Keeping the digit count
// beside the name means adding a roll is one table entry rather than a new
// handler plus a new command definition that can disagree with it.
type digitRoll struct {
	name        string
	description string
	digits      int32
}

var digitRolls = []digitRoll{
	{name: "doubles", description: "Roll for doubles", digits: 2},
	{name: "triples", description: "Roll for triples", digits: 3},
	{name: "quads", description: "Roll for quads", digits: 4},
	{name: "quints", description: "Roll for quints", digits: 5},
	{name: "sexts", description: "Roll for sexts", digits: 6},
}

// Number bounds. The server range is [lower, upper), so an upper of 10 yields
// 0-9.
const (
	numberDefaultLower int64 = 0
	numberDefaultUpper int64 = 10
)

func digitRollCommands() []command.Command {
	commands := make([]command.Command, 0, len(digitRolls))

	for _, roll := range digitRolls {
		commands = append(commands, command.Command{
			Name:        roll.name,
			Aliases:     localizedAliases(roll.name),
			Description: roll.description,
			Handler:     digitRollHandler(roll),
		})
	}

	return commands
}

func digitRollHandler(roll digitRoll) command.Handler {
	return func(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
		content, err := doublesPlusN(ctx, roll.digits)
		if err != nil {
			return nil, err
		}

		return &command.Response{
			Content:  content,
			ReRollID: reRollID(roll.name),
		}, nil
	}
}

func numberCommand() command.Command {
	return command.Command{
		Name:        "number",
		Description: "Roll a random number between an interval",
		Args: []command.Arg{
			{
				Name:        "lower",
				Description: "Lower bound, inclusive. Defaults to 0",
				Type:        command.ArgInt,
				Default:     numberDefaultLower,
			},
			{
				// The server range is [lower, upper), so the default of 10 yields
				// 0-9. The old description said "defaults to 9", which contradicted
				// the code and implied the bound was inclusive.
				Name:        "upper",
				Description: "Upper bound, exclusive. Defaults to 10",
				Type:        command.ArgInt,
				Default:     numberDefaultUpper,
			},
		},
		Handler: number,
	}
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

func doublesPlusN(ctx context.Context, digits int32) (string, error) {
	reqType := pb.GetRandomNumberReq_DOUBLES

	req := pb.GetRandomNumberReq_builder{
		Type:   &reqType,
		Digits: &digits,
	}.Build()

	resp, err := client.EntertainmentServiceClient.GetRandomNumber(ctx, req)
	if err != nil {
		log.Z.Error("failed to call GetRandomNumber", zap.Error(err))
		return "", err
	}

	msg := resp.GetNumber()

	// Hits are bolded. The roll is ASCII digits, so counting the first byte is
	// equivalent to counting the first character; an empty response would panic
	// on msg[0].
	if msg != "" && strings.Count(msg, msg[:1]) == len(msg) {
		msg = "**" + msg + "**"
	}

	return msg, nil
}

func number(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	reqType := pb.GetRandomNumberReq_INTERVAL

	lower := inv.Int("lower")
	upper := inv.Int("upper")

	req := pb.GetRandomNumberReq_builder{
		Type:  &reqType,
		Lower: &lower,
		Upper: &upper,
	}.Build()

	resp, err := client.EntertainmentServiceClient.GetRandomNumber(ctx, req)
	if err != nil {
		log.Z.Error("failed to call GetRandomNumber", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content: "**" + resp.GetNumber() + "** \U0001F3B2",
	}, nil
}
