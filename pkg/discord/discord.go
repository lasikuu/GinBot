package discord

import (
	"os"
	"os/signal"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

var discordSession *discordgo.Session

func InitializeDiscord() {
	var err error
	if discordSession, err = discordgo.New(config.Options.Discord.BotToken); err != nil {
		log.Z.Fatal("cannot create a new session.", zap.Error(err))
	}

	discordSession.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Z.Info("logged in as.", zap.String("username", s.State.User.Username))
	})

	discordSession.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
				h(s, i)
			}
		case discordgo.InteractionMessageComponent:
			if h, ok := componentHandlers[i.MessageComponentData().CustomID]; ok {
				h(s, i)
			}
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
