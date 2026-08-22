package server

import (
	"context"

	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ContextMetadata is the caller identity carried on every request.
type ContextMetadata = callermeta.Caller

// getMetadata extracts caller identity from the incoming gRPC metadata.
// The encoding is owned by pkg/grpc/callermeta so that clients and the server
// cannot disagree about it.
func getMetadata(ctx context.Context) (*ContextMetadata, error) {
	return callermeta.FromIncomingContext(ctx)
}

// callerUser returns the user_account row the clearance interceptor already
// resolved for this request.
//
// Handlers must not look the caller up again: the interceptor has done it once
// per request, and a second query is both wasted work and an opportunity for
// the two lookups to disagree.
//
// A missing caller is a wiring fault, not a client fault — it means the method
// was left out of interceptor.DefaultRequirements and is running unguarded — so
// it fails closed with Internal rather than letting the handler proceed
// anonymously.
func callerUser(ctx context.Context) (*model.User, error) {
	caller, ok := interceptor.CallerFromContext(ctx)
	if !ok {
		log.Z.Error("handler ran without a resolved caller; the method is missing from the clearance requirements")
		return nil, status.Errorf(codes.Internal, "caller was not resolved")
	}

	return caller, nil
}
