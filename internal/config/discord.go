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
	// MessageContent opts in to the privileged MESSAGE_CONTENT gateway intent
	// on its own. Trigger matching requires it: without the intent every
	// MessageCreate arrives with an empty Content, so there is nothing to match
	// a phrase against. Chat commands already imply it, so this only matters for
	// a deployment that wants triggers without any chat prefix configured.
	//
	// It must stay opt-in. The intent has to be enabled for the application in
	// the Discord developer portal first, and requesting it when it is not makes
	// the gateway close with 4014 — a bot that cannot start at all.
	MessageContent bool
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

func messageContent() bool {
	return os.Getenv("DISCORD_MESSAGE_CONTENT") == "true"
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
