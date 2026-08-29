package clientopts

import (
	"testing"

	"github.com/lasikuu/GinBot/pkg/grpc/server"
)

// A chunk larger than messageBytes makes GetFile fail on the client side.
func TestMessageBytesExceedsTheGetFileChunkSize(t *testing.T) {
	if messageBytes <= server.GetFileChunkBytes {
		t.Errorf("messageBytes = %d, want strictly greater than server.GetFileChunkBytes (%d); "+
			"the platform clients would refuse every GetFileChunk the server sends",
			messageBytes, server.GetFileChunkBytes)
	}
}
