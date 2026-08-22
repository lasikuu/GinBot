package server

import (
	"context"

	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// ContextMetadata is the caller identity carried on every request.
type ContextMetadata = callermeta.Caller

// getMetadata extracts caller identity from the incoming gRPC metadata.
// The encoding is owned by pkg/grpc/callermeta so that clients and the server
// cannot disagree about it.
func getMetadata(ctx context.Context) (*ContextMetadata, error) {
	return callermeta.FromIncomingContext(ctx)
}
