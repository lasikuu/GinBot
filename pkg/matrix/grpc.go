package matrix

import (
	"context"

	"maunium.net/go/mautrix/id"

	"github.com/lasikuu/GinBot/internal/clientopts"
	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// clientsContextKey types the context value carrying the service clients a
// handler runs against, so it cannot collide with another package's key.
type clientsContextKey struct{}

// withClients attaches the service clients to ctx.
func withClients(ctx context.Context, c *client.Clients) context.Context {
	return context.WithValue(ctx, clientsContextKey{}, c)
}

// clientsFrom returns the service clients the handler context carries. It
// returns nil when none were attached, which is a wiring bug.
//
// This mirrors pkg/discord deliberately, including the fact that nothing in
// this package reads it yet: actionHandlers registers only handleSendTest,
// which makes no RPC. The seam exists for the same reason startActionStream
// does — the first Matrix notification handler will need ConfirmDelivery
// exactly as Discord's does, and it should find a client waiting rather than
// have to invent a way to reach one.
func clientsFrom(ctx context.Context) *client.Clients {
	c, _ := ctx.Value(clientsContextKey{}).(*client.Clients)
	return c
}

// NewMatrixClient dials the Connect boundary and builds every service client
// this package needs.
//
// It deliberately does NOT start the reverse action stream. That happens in
// InitializeMatrix, once matrixClient exists — see startActionStream.
//
// The context parameter is therefore unused, and is retained only to keep this
// signature identical to NewDiscordClient's, which is unused for the same
// reason. cmd/ginbot-matrix still passes ctx, so dropping it here would make the
// two binaries diverge for no gain — do not "clean it up" in one without the
// other.
func NewMatrixClient(_ context.Context) (*client.Clients, error) {
	opts, err := clientopts.Dial()
	if err != nil {
		return nil, err
	}

	return client.Dial(opts)
}

// matrixStreamIdentity is the identity this bot process asserts to open the
// reverse action stream — its own Matrix account, not any invoking user's.
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

// startActionStream begins consuming server-pushed actions.
//
// ORDER MATTERS, and this function exists to make that order impossible to get
// wrong. Action handlers run on the stream's own goroutine, while matrixClient is
// written once by InitializeMatrix with nothing synchronising it — so starting
// the stream before that assignment is both a data race on the package variable
// and a nil dereference in any handler that reads it. Launching the goroutine
// after the assignment gives the read a happens-before edge on the write.
//
// Unlike the Discord equivalent the hazard here is prophylactic, not live: no
// entry in actionHandlers touches matrixClient today, so nothing crashes right
// now. It becomes real with the first Matrix notification handler, and the
// stream used to be started from NewMatrixClient — which cmd/ginbot-matrix calls
// BEFORE InitializeMatrix — so writing that handler would have been enough to
// break it. This seam is what makes writing one safe rather than merely lucky.
//
// It requires clients to have already been dialed. clients is attached to the
// stream's own context — not a package global — so an action handler that
// makes its own RPC can reach it through clientsFrom, exactly as the Discord
// equivalent does.
func startActionStream(ctx context.Context, clients *client.Clients) {
	ctx = withClients(ctx, clients)
	go clients.RunClientActionStream(ctx, matrixStreamIdentity(), actionHandlers())
}
