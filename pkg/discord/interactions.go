package discord

import (
	"context"
	"errors"
	"slices"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/gen/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"google.golang.org/grpc/metadata"
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
	md := metadata.Pairs(
		"platform_enum", proto.PlatformEnum_DISCORD.String(),
		"user_id", userID,
	)
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	return ctx, nil
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
