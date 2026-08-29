package matrix

import (
	"context"
	"testing"

	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// withMatrixUserID restores the previous value because config.Options is a
// package-level pointer that a later test in this package would otherwise see.
func withMatrixUserID(t *testing.T, mxid string) {
	t.Helper()

	previous := config.Options
	config.Options = &config.OptionsModel{Matrix: config.MatrixOptions{UserID: mxid}}
	t.Cleanup(func() { config.Options = previous })
}

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
			// id.UserID.Localpart validates nothing: with no ':' it returns
			// everything after the first character, so this yields "ot-an-mxid"
			// rather than falling back.
			name:         "a colon-less string is not rejected, just misparsed",
			mxid:         "not-an-mxid",
			wantUsername: "ot-an-mxid",
		},
		{
			// The one shape producing an empty localpart, which is what the
			// fallback exists for.
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
			// PlatformUID is the full MXID: it is what the server matches on.
			if got.PlatformUID != tt.mxid {
				t.Errorf("PlatformUID = %q, want the full MXID %q unmodified", got.PlatformUID, tt.mxid)
			}
			if got.Username != tt.wantUsername {
				t.Errorf("Username = %q, want %q", got.Username, tt.wantUsername)
			}
		})
	}
}

func TestClientsFromReturnsWhatWithClientsAttached(t *testing.T) {
	c := &client.Clients{}

	ctx := withClients(context.Background(), c)

	if got := clientsFrom(ctx); got != c {
		t.Errorf("clientsFrom returned %p, want the same *Clients withClients attached (%p)", got, c)
	}
}

func TestClientsFromReturnsNilForABareContext(t *testing.T) {
	if got := clientsFrom(context.Background()); got != nil {
		t.Errorf("clientsFrom(bare context) = %v, want nil", got)
	}
}
