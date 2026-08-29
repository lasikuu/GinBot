package discord

import (
	"context"

	"github.com/lasikuu/GinBot/internal/clientopts"
	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
)

type clientsContextKey struct{}

func withClients(ctx context.Context, c *client.Clients) context.Context {
	return context.WithValue(ctx, clientsContextKey{}, c)
}

// clientsFrom returns the handler context's service clients, or nil when none
// were attached — a wiring bug, since every RPC path attaches them.
func clientsFrom(ctx context.Context) *client.Clients {
	c, _ := ctx.Value(clientsContextKey{}).(*client.Clients)
	return c
}

// NewDiscordClient dials the Connect boundary and builds every service client.
// It does not start the reverse action stream; see startActionStream.
func NewDiscordClient(_ context.Context) (*client.Clients, error) {
	opts, err := clientopts.Dial()
	if err != nil {
		return nil, err
	}

	return client.Dial(opts)
}

// discordStreamIdentity is the identity this process asserts to open the reverse
// action stream — its own application account, not an invoking user's. The
// ClientId and "ginbot" fallbacks cover gateway state that has not arrived yet.
func discordStreamIdentity() client.StreamIdentity {
	var platformUID, username string

	if discordSession != nil && discordSession.State != nil && discordSession.State.User != nil {
		platformUID = discordSession.State.User.ID
		username = discordSession.State.User.Username
	}
	if platformUID == "" {
		platformUID = config.Options.Discord.ClientId
	}
	if username == "" {
		username = "ginbot"
	}

	// An empty uid omits ginbot-user-id from the header, so Register fails and
	// the server refuses the stream for the process's life — logged distinctly
	// so it is not mistaken for an unreachable server.
	if platformUID == "" {
		log.Z.Error("no Discord identity for the reverse action stream; " +
			"the gateway session has no user and DISCORD_CLIENT_ID is unset, " +
			"so this process cannot register and will never receive reminders")
	}

	return client.StreamIdentity{
		Platform:    pb.Platform_PLATFORM_DISCORD,
		PlatformUID: platformUID,
		Username:    username,
	}
}

// startActionStream begins consuming server-pushed actions. It must run only
// after discordSession is assigned: handlers read it, so an earlier start is a
// data race and a nil deref. clients is attached to the stream's context so a
// handler making its own RPC can reach it via clientsFrom.
func startActionStream(ctx context.Context, clients *client.Clients) {
	ctx = withClients(ctx, clients)
	go clients.RunClientActionStream(ctx, discordStreamIdentity(), actionHandlers())
}
