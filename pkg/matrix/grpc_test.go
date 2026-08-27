package matrix

import (
	"context"
	"testing"

	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// withMatrixUserID sets config.Options.Matrix.UserID for one test and restores
// whatever was there, without going through config.LoadEnv/SetEnv — this
// package's tests otherwise never touch internal/config at all, and pulling
// in the full env-loading startup sequence just to exercise one pure
// derivation would be a heavier dependency than the seam is worth.
//
// config.Options is a package-level pointer read without synchronisation
// elsewhere in the binary, which is why it is restored rather than left
// mutated: an assignment that outlived this test would be visible to any test
// in this package that runs after it, in this or a future file.
func withMatrixUserID(t *testing.T, mxid string) {
	t.Helper()

	previous := config.Options
	config.Options = &config.OptionsModel{Matrix: config.MatrixOptions{UserID: mxid}}
	t.Cleanup(func() { config.Options = previous })
}

// TestMatrixStreamIdentityDerivesTheUsernameFromTheMXIDLocalpart is the one
// cheap seam in this package's Connect-port surface that is not otherwise
// covered: matrixStreamIdentity's job is entirely this derivation plus a
// fallback, with no homeserver connection and no package-global session state
// involved, unlike everything else pkg/matrix now wires through
// client.Clients.
//
// The username matters beyond cosmetics: it is what ensureRegistered
// (pkg/grpc/client) sends as RegisterReq.username the first time this bot
// process registers itself, so a wrong derivation here does not just log
// oddly, it names the bot's own account wrong on every fresh deployment.
func TestMatrixStreamIdentityDerivesTheUsernameFromTheMXIDLocalpart(t *testing.T) {
	tests := []struct {
		name         string
		mxid         string
		wantUsername string
	}{
		{
			name:         "an ordinary MXID yields its localpart",
			mxid:         "@ginbot:example.org",
			wantUsername: "ginbot",
		},
		{
			name:         "a localpart containing punctuation is preserved",
			mxid:         "@gin.bot_1:example.org",
			wantUsername: "gin.bot_1",
		},
		{
			// id.UserID.Localpart does NOT validate the sigil or reject a
			// missing homeserver — ParseCommonIdentifier only special-cases
			// finding a ':', and otherwise blindly returns everything after
			// the first character as the localpart, whatever that character
			// is. "not-an-mxid" therefore does not fall back at all; it
			// yields "ot-an-mxid". This is deliberately pinned rather than
			// assumed, since it is the opposite of what a caller might expect
			// from a function named Localpart on a malformed input.
			name:         "a colon-less string is not rejected, just misparsed",
			mxid:         "not-an-mxid",
			wantUsername: "ot-an-mxid",
		},
		{
			// The one shape that DOES produce an empty localpart: nothing
			// after the leading character at all, so slicing from index 1
			// onward is the empty string. This is the case
			// matrixStreamIdentity's fallback actually exists for.
			name:         "a bare sigil with nothing after it falls back to the generic username",
			mxid:         "@",
			wantUsername: "ginbot",
		},
		{
			name:         "an empty MXID falls back to the generic username",
			mxid:         "",
			wantUsername: "ginbot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withMatrixUserID(t, tt.mxid)

			got := matrixStreamIdentity()

			if got.Platform != pb.Platform_PLATFORM_MATRIX_PROTOCOL {
				t.Errorf("Platform = %v, want %v", got.Platform, pb.Platform_PLATFORM_MATRIX_PROTOCOL)
			}
			// PlatformUID is the full MXID, unmodified — it is what the server
			// stores and matches callers by, not something to localpart-derive.
			if got.PlatformUID != tt.mxid {
				t.Errorf("PlatformUID = %q, want the full MXID %q unmodified", got.PlatformUID, tt.mxid)
			}
			if got.Username != tt.wantUsername {
				t.Errorf("Username = %q, want %q", got.Username, tt.wantUsername)
			}
		})
	}
}

// ── withClients / clientsFrom ────────────────────────────────────────────────
//
// This is the seam startActionStream attaches *client.Clients through
// (grpc.go), mirroring pkg/discord's identical pair. Nothing in this package
// reads it back yet — actionHandlers registers only handleSendTest, which
// makes no RPC — but the round trip through context.Value is exactly the kind
// of thing that silently breaks on a typed-key or a wrong-type-assertion typo
// with no compile error to catch it, and the first Matrix notification
// handler that DOES call clientsFrom (as startActionStream's own doc comment
// says it will) needs this to already be right rather than to discover it is
// not.

// TestClientsFromReturnsWhatWithClientsAttached is the round trip: whatever
// *client.Clients withClients attached is exactly what clientsFrom hands
// back, same pointer, from a context carrying nothing else.
func TestClientsFromReturnsWhatWithClientsAttached(t *testing.T) {
	c := &client.Clients{}

	ctx := withClients(context.Background(), c)

	if got := clientsFrom(ctx); got != c {
		t.Errorf("clientsFrom returned %p, want the same *Clients withClients attached (%p)", got, c)
	}
}

// TestClientsFromReturnsNilForABareContext: a context nobody ever attached
// clients to is a wiring bug, not a value clientsFrom should synthesise or
// panic on — the failed type assertion inside it must degrade to nil rather
// than to a panic that would take down whatever goroutine calls it.
func TestClientsFromReturnsNilForABareContext(t *testing.T) {
	if got := clientsFrom(context.Background()); got != nil {
		t.Errorf("clientsFrom(bare context) = %v, want nil", got)
	}
}
