package discord

import (
	"context"

	"github.com/lasikuu/GinBot/internal/clientopts"
	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
)

// clientsContextKey types the context value carrying the service clients a
// handler runs against, so it cannot collide with another package's key.
type clientsContextKey struct{}

// withClients attaches the service clients to ctx.
func withClients(ctx context.Context, c *client.Clients) context.Context {
	return context.WithValue(ctx, clientsContextKey{}, c)
}

// clientsFrom returns the service clients the handler context carries. It
// returns nil when none were attached, which is a wiring bug: every path that
// reaches an RPC goes through commandContext or startActionStream.
func clientsFrom(ctx context.Context) *client.Clients {
	c, _ := ctx.Value(clientsContextKey{}).(*client.Clients)
	return c
}

// NewDiscordClient dials the Connect boundary and builds every service client
// this package needs.
//
// It deliberately does NOT start the reverse action stream. That happens in
// InitializeDiscord, once the Discord session exists — see startActionStream.
func NewDiscordClient(_ context.Context) (*client.Clients, error) {
	opts, err := clientopts.Dial()
	if err != nil {
		return nil, err
	}

	return client.Dial(opts)
}

// discordStreamIdentity is the identity this bot process asserts to open the
// reverse action stream — its own Discord application account, not any
// invoking user's.
//
// discordSession.State.User is populated only once the gateway session is
// Open, so config.Options.Discord.ClientId is the fallback for state that has
// not arrived yet, and "ginbot" is the last-resort username the server needs
// to register a caller it has never seen. startActionStream runs after Open,
// so in practice discordSession.State.User is what actually gets used; the
// fallbacks exist for defensiveness rather than the expected path.
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

	// An empty uid is not survivable and must not be allowed to look like a
	// network problem. callermeta.WriteHeader omits ginbot-user-id entirely for
	// an empty value, so Register fails and the server then refuses the stream
	// for the rest of the process's life — with logs indistinguishable from a
	// server that is merely unreachable. Saying so once, here, is the
	// difference between a five-minute fix and an afternoon.
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

// startActionStream begins consuming server-pushed actions.
//
// ORDER MATTERS, and this function exists to make that order impossible to get
// wrong. Action handlers run on the stream's own goroutine and a notification
// handler uses discordSession, so the stream must not start until that variable
// has been assigned: starting it first is both a data race on the package
// variable and a nil dereference in the handler that reads it. Launching the
// goroutine after the assignment gives the read a happens-before edge on the
// write.
//
// The hazard here is live, not prophylactic — unlike the Matrix equivalent,
// handleSendNotification reads discordSession today, so getting the order wrong
// breaks the first reminder that arrives rather than some future one. What it
// costs is now bounded: pkg/grpc/client.dispatch recovers around the inline
// handler call, so the deref loses that one delivery instead of killing the
// process. Bounded is not acceptable, which is why the seam stays.
//
// It requires clients to have already been dialed. clients is attached to the
// stream's own context — not a package global — so an action handler that
// makes its own RPC (confirmDelivery, today) can reach it through
// clientsFrom.
func startActionStream(ctx context.Context, clients *client.Clients) {
	ctx = withClients(ctx, clients)
	go clients.RunClientActionStream(ctx, discordStreamIdentity(), actionHandlers())
}
