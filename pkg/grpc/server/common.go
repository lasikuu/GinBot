package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/log"
)

// ContextMetadata is the caller identity carried on every request.
type ContextMetadata = callermeta.Caller

// getMetadata returns the caller's asserted platform identity, stashed into
// the context by interceptor.ClearanceInterceptor for every call whose
// headers parsed.
//
// It must only be called from a guarded handler (or from a helper only guarded
// handlers reach, such as callerOriginInstanceID in trigger.go): the
// interceptor stashes best-effort for public methods too, but a public method
// that needs identity of its own — UserService/Register — has the request
// object directly in scope and should read its headers through
// callermeta.FromHeader itself, not through this indirection. Its absence here
// is therefore a wiring fault, not a client fault, and fails closed the same
// way callerUser does.
func getMetadata(ctx context.Context) (*ContextMetadata, error) {
	meta, ok := interceptor.MetaFromContext(ctx)
	if !ok {
		log.Z.Error("handler asked for caller metadata that was never stashed in the context")
		return nil, connect.NewError(connect.CodeInternal, errors.New("caller metadata was not resolved"))
	}

	return meta, nil
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
		return nil, connect.NewError(connect.CodeInternal, errors.New("caller was not resolved"))
	}

	return caller, nil
}
