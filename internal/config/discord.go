package config

import (
	"os"
)

type DiscordOptions struct {
	OwnerId  string
	BotToken string
	ClientId string
}

func ownerId() string {
	return os.Getenv("DISCORD_OWNER_ID")
}

func botToken() string {
	return os.Getenv("DISCORD_BOT_TOKEN")
}

func clientId() string {
	return os.Getenv("DISCORD_CLIENT_ID")
}
