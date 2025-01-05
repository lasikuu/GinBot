package discord

import (
	"context"
	"errors"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
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
		"user_id", userID,
		"user_clearance", "0", // TODO: Implement user clearance (e.g. 0: user, 10: member, 50: admin, 100: owner)
	)
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	return ctx, nil
}

var (
	commands = []*discordgo.ApplicationCommand{
		{
			Name:        "healthcheck",
			Description: "Health check of services such as DB.",
			NameLocalizations: &map[discordgo.Locale]string{
				discordgo.Japanese: "ヘルスチェック",
			},
			DescriptionLocalizations: &map[discordgo.Locale]string{
				discordgo.Japanese: "DBなどのサービスのヘルスチェック。",
			},
		},
	}

	commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"healthcheck": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			ctx, err := interactionContext(i)
			if err != nil {
				log.Z.Error("failed to get context.", zap.Error(err))
				return
			}

			resp, err := client.UtilityServiceClient.HealthCheck(ctx, &emptypb.Empty{})
			if err != nil {
				log.Z.Error("failed to call HealthCheck.", zap.Error(err))
				return
			}

			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: resp.GetStatus().String(),
				},
			})
			if err != nil {
				log.Z.Error("failed to respond to HealthCheck.", zap.Error(err))
			}
		},
	}
)
