package matrix

import (
	"context"

	"maunium.net/go/mautrix/id"

	"github.com/lasikuu/GinBot/internal/clientopts"
	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

type clientsContextKey struct{}

func withClients(ctx context.Context, c *client.Clients) context.Context {
	return context.WithValue(ctx, clientsContextKey{}, c)
}

// clientsFrom returns nil when no clients were attached, which is a wiring bug.
func clientsFrom(ctx context.Context) *client.Clients {
	c, _ := ctx.Value(clientsContextKey{}).(*client.Clients)
	return c
}

// NewMatrixClient does not start the reverse action stream; InitializeMatrix
// does, once matrixClient exists. The unused ctx keeps the signature identical
// to NewDiscordClient's.
func NewMatrixClient(_ context.Context) (*client.Clients, error) {
	opts, err := clientopts.Dial()
	if err != nil {
		return nil, err
	}

	return client.Dial(opts)
}

// matrixStreamIdentity is the bot's own account, not any invoking user's.
func matrixStreamIdentity() client.StreamIdentity {
	mxid := config.Options.Matrix.UserID

	username := id.UserID(mxid).Localpart()
	if username == "" {
		username = "ginbot"
	}

	return client.StreamIdentity{
		Platform:    pb.Platform_PLATFORM_MATRIX_PROTOCOL,
		PlatformUID: mxid,
		Username:    username,
	}
}

// startActionStream must be called after matrixClient is assigned: handlers run
// on the stream goroutine, and launching it later gives that read a
// happens-before edge on the write. clients must already be dialed.
func startActionStream(ctx context.Context, clients *client.Clients) {
	ctx = withClients(ctx, clients)
	go clients.RunClientActionStream(ctx, matrixStreamIdentity(), actionHandlers())
}
