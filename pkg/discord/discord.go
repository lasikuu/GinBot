package discord

import (
	"os"
	"os/signal"
	"syscall"

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

	initCommands()

	discordSession.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Z.Info("logged in as.", zap.String("username", s.State.User.Username))
	})

	discordSession.AddHandler(handleInteraction)

	// Chat commands are opt-in, because they cost a privileged intent.
	//
	// MESSAGE_CONTENT is not in discordgo's default set, so without it every
	// MessageCreate arrives with an empty Content and nothing can ever match.
	// Requesting it when it is not enabled for the application in the Discord
	// developer portal makes the gateway close with 4014, which surfaces here
	// as a fatal "cannot open the session" — so requesting it unconditionally
	// would stop the bot booting for anyone who does not use chat commands.
	if len(config.Options.Discord.CommandPrefixes.Prefixes) > 0 {
		discordSession.Identify.Intents |= discordgo.IntentMessageContent
		discordSession.AddHandler(handleMessage)
	} else {
		log.Z.Warn("no command prefixes configured, chat commands are disabled.")
	}

	if err = discordSession.Open(); err != nil {
		log.Z.Fatal("cannot open the session.", zap.Error(err))
	}

	commands := applicationCommands(commandRegistry)
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
	// SIGTERM as well as SIGINT, so container shutdowns are graceful.
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
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
