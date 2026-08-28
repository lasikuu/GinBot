package clientopts

import (
	"testing"

	"github.com/lasikuu/GinBot/pkg/grpc/server"
)

// TestMessageBytesExceedsTheGetFileChunkSize is the client half of the
// invariant cmd/ginbot-server asserts for its own baselineMessageBytes.
//
// Deleting config.MaxGRPCMessageBytes replaced one shared constant with a
// per-binary one, so the relationship that used to be structural is now only
// written down. It still has to hold: messageBytes is installed as
// connect.WithReadMaxBytes on every client this process dials, so a chunk
// larger than it makes TriggerService/GetFile fail at the transport on the
// CLIENT side — with the server having done nothing wrong and having no way to
// report it — for every trigger file playback.
//
// Importing pkg/grpc/server here is a test-only dependency and does not put
// the server into either platform client's binary.
func TestMessageBytesExceedsTheGetFileChunkSize(t *testing.T) {
	if messageBytes <= server.GetFileChunkBytes {
		t.Errorf("messageBytes = %d, want strictly greater than server.GetFileChunkBytes (%d); "+
			"the platform clients would refuse every GetFileChunk the server sends",
			messageBytes, server.GetFileChunkBytes)
	}
}
