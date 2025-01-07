package discord

import (
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/emptypb"
)

var UtilityCommands = []*discordgo.ApplicationCommand{
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

func HealthCheck(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
}
