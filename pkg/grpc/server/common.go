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

// getMetadata returns the caller identity ClearanceInterceptor stashed. Only
// valid from a guarded handler; a public method must use callermeta.FromHeader.
func getMetadata(ctx context.Context) (*ContextMetadata, error) {
	meta, ok := interceptor.MetaFromContext(ctx)
	if !ok {
		log.Z.Error("handler asked for caller metadata that was never stashed in the context")
		return nil, connect.NewError(connect.CodeInternal, errors.New("caller metadata was not resolved"))
	}

	return meta, nil
}

// callerUser returns the row the clearance interceptor already resolved. Absence
// means the method is missing from interceptor.DefaultRequirements: it fails closed.
func callerUser(ctx context.Context) (*model.User, error) {
	caller, ok := interceptor.CallerFromContext(ctx)
	if !ok {
		log.Z.Error("handler ran without a resolved caller; the method is missing from the clearance requirements")
		return nil, connect.NewError(connect.CodeInternal, errors.New("caller was not resolved"))
	}

	return caller, nil
}
