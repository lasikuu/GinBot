package config

import (
	"os"
	"strings"
)

type DiscordOptions struct {
	OwnerId         string
	BotToken        string
	ClientId        string
	EraseCommands   bool
	CommandPrefixes CommandPrefixes
	// MessageContent must stay opt-in: requesting the privileged intent before
	// it is enabled in the developer portal closes the gateway with 4014.
	MessageContent bool
}

// CommandPrefixes with an empty Prefixes slice disables chat commands.
type CommandPrefixes struct {
	Prefixes []string
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

func messageContent() bool {
	return os.Getenv("DISCORD_MESSAGE_CONTENT") == "true"
}

// Empty elements are dropped: an empty prefix would match every message.
func commandPrefixes() CommandPrefixes {
	configured := strings.Split(os.Getenv("DISCORD_COMMAND_PREFIXES"), ",")

	prefixes := make([]string, 0, len(configured))
	for _, prefix := range configured {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		prefixes = append(prefixes, prefix)
	}

	return CommandPrefixes{Prefixes: prefixes}
}
