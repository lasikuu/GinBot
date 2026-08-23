package discord

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// discordSession is written once by InitializeDiscord and then read from
// several goroutines (discordgo's dispatch goroutines, and the reverse action
// stream). Nothing synchronises it, so it must be assigned BEFORE any of those
// readers is started — see startActionStream.
var discordSession *discordgo.Session

// InitializeDiscord brings up the Discord session and blocks until the process
// is signalled to stop.
//
// ctx bounds the reverse action stream, which is started from here rather than
// alongside the gRPC clients precisely because its handlers read
// discordSession.
func InitializeDiscord(ctx context.Context) {
	var err error
	if discordSession, err = discordgo.New(config.Options.Discord.BotToken); err != nil {
		log.Z.Fatal("cannot create a new session.", zap.Error(err))
	}

	initCommands()

	discordSession.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Z.Info("logged in as.", zap.String("username", s.State.User.Username))
	})

	discordSession.AddHandler(handleInteraction)

	// Reading messages at all is opt-in, because it costs a privileged intent.
	//
	// MESSAGE_CONTENT is not in discordgo's default set, so without it every
	// MessageCreate arrives with an empty Content and nothing can ever match.
	// Requesting it when it is not enabled for the application in the Discord
	// developer portal makes the gateway close with 4014, which surfaces here
	// as a fatal "cannot open the session" — so requesting it unconditionally
	// would stop the bot booting for anyone who does not use the message path.
	//
	// THREE capabilities ride on it, and any one is enough: chat commands need
	// the content to find their prefix, trigger matching needs it to match a
	// phrase, and WANHA needs both Content and Attachments populated at all.
	// So the decision is not "are chat prefixes configured" — a deployment
	// that only wants triggers or only wants WANHA sets DISCORD_MESSAGE_CONTENT
	// or GINBOT_REPOST instead. The repost half is OR'd in here rather than
	// folded into messageContentRequired's own signature, which
	// TestMessageContentRequired exercises directly with two arguments.
	if messageContentRequired(config.Options.Discord.CommandPrefixes.Prefixes, config.Options.Discord.MessageContent) || config.Options.Repost.Enabled {
		discordSession.Identify.Intents |= discordgo.IntentMessageContent
		discordSession.AddHandler(handleMessage)
		discordSession.AddHandler(handleMessageUpdate)
	} else {
		log.Z.Warn("MESSAGE_CONTENT intent not requested, so chat commands, trigger matching and WANHA are all disabled. " +
			"Set DISCORD_COMMAND_PREFIXES, DISCORD_MESSAGE_CONTENT=true or GINBOT_REPOST=true, and enable the intent in the Discord developer portal.")
	}

	if err = discordSession.Open(); err != nil {
		log.Z.Fatal("cannot open the session.", zap.Error(err))
	}

	// Only now: discordSession is assigned and connected, so a notification
	// arriving on the first tick has a session to post through.
	startActionStream(ctx)

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
