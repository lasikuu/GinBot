package discord

import (
	"context"
	"os"
	"os/signal"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

var discordSession *discordgo.Session

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
			// TODO: Separate setting metadata into its own function
			var userID string
			if i.Member != nil {
				userID = i.Member.User.ID
			} else if i.User != nil {
				userID = i.User.ID
			} else {
				log.Z.Error("cannot get user ID.")
				return
			}
			md := metadata.Pairs(
				"user_id", userID,
				"user_clearance", "0", // TODO: Implement user clearance
			)
			ctx := metadata.NewOutgoingContext(context.Background(), md)

			resp, err := client.UtilityServiceClient.HealthCheck(ctx, &emptypb.Empty{})
			if err != nil {
				log.Z.Error("failed to call HealthCheck.", zap.Error(err))
				return
			}

			response := resp.GetStatus().String()

			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: response,
				},
			})
			if err != nil {
				panic(err)
			}
		},
	}
)

func InitializeDiscord() {
	var err error
	if discordSession, err = discordgo.New(config.Options.Discord.BotToken); err != nil {
		log.Z.Fatal("cannot create a new session.", zap.Error(err))
	}

	discordSession.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Z.Info("logged in as.", zap.String("username", s.State.User.Username))
	})

	discordSession.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
			h(s, i)
		}
	})

	if err = discordSession.Open(); err != nil {
		log.Z.Fatal("cannot open the session.", zap.Error(err))
	}

	registeredCommands := make([]*discordgo.ApplicationCommand, len(commands))
	for i, v := range commands {
		cmd, err := discordSession.ApplicationCommandCreate(discordSession.State.User.ID, "", v)
		if err != nil {
			log.Z.Fatal("Cannot create command.", zap.String("command", v.Name), zap.Error(err))
		}
		registeredCommands[i] = cmd
	}

	defer func(discordSession *discordgo.Session) {
		err := discordSession.Close()
		if err != nil {
			log.Z.Fatal("could not close the session gracefully.", zap.Error(err))
		}
	}(discordSession)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop

	if config.Options.Discord.EraseCommands {
		log.Z.Info("removing commands.")

		for _, v := range registeredCommands {
			err := discordSession.ApplicationCommandDelete(discordSession.State.User.ID, "", v.ID)
			if err != nil {
				log.Z.Fatal("cannot delete command.", zap.String("command", v.Name), zap.Error(err))
			}
		}
	}

	log.Z.Info("gracefully shutting down.")
}
