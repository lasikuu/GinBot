package discord

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// discordSession is written once by InitializeDiscord and read from several
// goroutines with no synchronisation, so it must be assigned before any reader
// starts — see startActionStream.
var discordSession *discordgo.Session

// InitializeDiscord brings up the Discord session and blocks until the process
// is signalled to stop. ctx bounds the reverse action stream.
func InitializeDiscord(ctx context.Context, clients *client.Clients) {
	var err error
	if discordSession, err = discordgo.New(config.Options.Discord.BotToken); err != nil {
		log.Z.Fatal("cannot create a new session.", zap.Error(err))
	}

	initCommands()

	discordSession.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Z.Info("logged in as.", zap.String("username", s.State.User.Username))
	})

	discordSession.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		handleInteraction(s, i, clients)
	})

	// The privileged MESSAGE_CONTENT intent is opt-in: without it MessageCreate
	// arrives with empty Content, and requesting it when the portal has it
	// disabled closes the gateway with 4014. Chat commands, triggers and WANHA
	// each need it, so it is requested only when one of them is enabled.
	if messageContentRequired(config.Options.Discord.CommandPrefixes.Prefixes, config.Options.Discord.MessageContent) || config.Options.Repost.Enabled {
		discordSession.Identify.Intents |= discordgo.IntentMessageContent
		discordSession.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
			handleMessage(s, m, clients)
		})
		discordSession.AddHandler(func(s *discordgo.Session, m *discordgo.MessageUpdate) {
			handleMessageUpdate(s, m, clients)
		})
	} else {
		log.Z.Warn("MESSAGE_CONTENT intent not requested, so chat commands, trigger matching and WANHA are all disabled. " +
			"Set DISCORD_COMMAND_PREFIXES, DISCORD_MESSAGE_CONTENT=true or GINBOT_REPOST=true, and enable the intent in the Discord developer portal.")
	}

	if err = discordSession.Open(); err != nil {
		log.Z.Fatal("cannot open the session.", zap.Error(err))
	}

	// Only after Open: handlers on the stream read discordSession.
	startActionStream(ctx, clients)

	commands := applicationCommands(commandRegistry)
	registeredCommands := make([]*discordgo.ApplicationCommand, len(commands))
	for i, v := range commands {
		cmd, err := discordSession.ApplicationCommandCreate(discordSession.State.User.ID, "", v)
		if err != nil {
			log.Z.Fatal("Cannot create command.", zap.String("command", v.Name), zap.Error(err))
		}
		registeredCommands[i] = cmd
	}

	// Error, not Fatal: os.Exit would skip the caller's outstanding defers,
	// including the log.Sync that flushes this line.
	defer func(discordSession *discordgo.Session) {
		err := discordSession.Close()
		if err != nil {
			log.Z.Error("could not close the session gracefully.", zap.Error(err))
		}
	}(discordSession)

	stop := make(chan os.Signal, 1)
	// SIGTERM as well as SIGINT, so container shutdowns are graceful.
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	if config.Options.Discord.EraseCommands {
		log.Z.Info("removing commands.")

		// Error, not Fatal: one failed erase must not abandon the rest or skip
		// the deferred Close and the caller's log.Sync.
		for _, v := range registeredCommands {
			err := discordSession.ApplicationCommandDelete(discordSession.State.User.ID, "", v.ID)
			if err != nil {
				log.Z.Error("cannot delete command.", zap.String("command", v.Name), zap.Error(err))
			}
		}
	}

	log.Z.Info("gracefully shutting down.")
}
