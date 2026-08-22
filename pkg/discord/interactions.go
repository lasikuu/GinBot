package discord

import (
	"context"
	"errors"
	"slices"

	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
)

func interactionContext(i *discordgo.InteractionCreate) (context.Context, error) {
	var userID string
	if i.Member != nil {
		userID = i.Member.User.ID
	} else if i.User != nil {
		userID = i.User.ID
	} else {
		log.Z.Error("cannot get user id.")
		return context.Background(), errors.New("cannot get discord user id")
	}

	return callermeta.NewOutgoingContext(context.Background(), pb.Platform_PLATFORM_DISCORD, userID), nil
}

var (
	commands = slices.Concat(
		EntertainmentCommands,
		UtilityCommands,
	)

	commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"healthcheck": HealthCheck,
		"doubles":     Doubles,
		"triples":     Triples,
		"number":      Number,
	}

	componentHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"reRollDoubles": Doubles,
		"reRollTriples": Triples,
	}
)
