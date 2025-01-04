package discord

import (
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/internal/config"
)

// HandleMessage handles messages sent in Discord
func HandleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignores all messages created by bots and GinBot itself
	if m.Author.Bot || m.Author.ID == s.State.User.ID {
		return
	}

	// Ignores all messages that do not start with the command prefix
	if !config.Options.Discord.CommandPrefixes.PrefixRegex.MatchString(m.Content) {
		return
	}
}
