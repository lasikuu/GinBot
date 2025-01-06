package config

import (
	"os"
	"regexp"
	"strings"

	"github.com/lasikuu/GinBot/pkg/log"
)

type DiscordOptions struct {
	GRPCClientOptions GRPCClientOptions
	OwnerId           string
	BotToken          string
	ClientId          string
	EraseCommands     bool
	CommandPrefixes   CommandPrefixes
}

type CommandPrefixes struct {
	Prefixes    []string
	PrefixRegex *regexp.Regexp
}

func ownerId() string {
	return os.Getenv("DISCORD_OWNER_ID")
}

func botToken() string {
	return "Bot " + os.Getenv("DISCORD_BOT_TOKEN")
}

func clientId() string {
	return os.Getenv("DISCORD_CLIENT_ID")
}

func eraseCommands() bool {
	return os.Getenv("DISCORD_REMOVE_COMMANDS") == "true"
}

func commandPrefixes() CommandPrefixes {
	prefixes := strings.Split(os.Getenv("DISCORD_COMMAND_PREFIXES"), ",")
	prefixesFormatted := strings.Join(prefixes, "|")
	commandPrefixRegex, err := regexp.Compile(`^(` + regexp.QuoteMeta(prefixesFormatted) + `).+$`)
	if err != nil {
		log.S.Fatal("error parsing command prefixes: ", err)
	}

	return CommandPrefixes{
		Prefixes:    prefixes,
		PrefixRegex: commandPrefixRegex,
	}
}
