package config

import (
	"os"
	"strings"
)

type DiscordOptions struct {
	GRPCClientOptions GRPCClientOptions
	OwnerId           string
	BotToken          string
	ClientId          string
	EraseCommands     bool
	CommandPrefixes   CommandPrefixes
}

// CommandPrefixes holds the configured chat command prefixes. An empty
// Prefixes slice means chat commands are disabled.
//
// There is deliberately no compiled regex here. One used to exist, and it
// disagreed with the tokeniser that actually dispatches: `^(??).+$` rejects
// "?\nping" (a dot excludes newline) and accepts "? " (a space satisfies .+),
// whereas command.ParseChat does the opposite in both cases. Two subtly
// different answers to "is this a command" is worse than one.
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

// commandPrefixes parses the chat command prefixes.
//
// strings.Split of an unset variable yields one empty element, and an empty
// prefix would match every message. Dropping empty elements is therefore
// load-bearing: with nothing configured, chat commands are disabled rather
// than triggered by everything.
func commandPrefixes() CommandPrefixes {
	configured := strings.Split(os.Getenv("DISCORD_COMMAND_PREFIXES"), ",")

	prefixes := make([]string, 0, len(configured))
	for _, prefix := range configured {
		// Trimmed so that "??, !" does not yield the prefix " !".
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		prefixes = append(prefixes, prefix)
	}

	return CommandPrefixes{Prefixes: prefixes}
}
