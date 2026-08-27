package interceptor

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// Requirements maps a Connect procedure to the minimum clearance required.
// Procedures absent from the map are public: no caller is resolved for them.
//
// Keys must be the generated ginbotv1connect.*Procedure constants. A
// hand-written string that drifts from the generated one still compiles and
// silently makes the RPC public, which is a security failure rather than a
// bug.
type Requirements map[string]pb.Clearance

// CallerResolver looks a platform identity up. It is injected so the
// interceptor is testable without a database, and it matches the signature of
// db.GetUserByPlatformUID.
//
// It must report an unknown identity as db.ErrNotFound; any other error is
// treated as a failure to reach the store, not as an unregistered caller.
type CallerResolver func(ctx context.Context, platform pb.Platform, platformUID string) (*model.User, error)

// callerContextKey types the context value so it cannot collide with another
// package's key.
type callerContextKey struct{}

// metaContextKey holds the caller's raw ASSERTED identity — platform and
// platform UID, unresolved against the database. ClearanceInterceptor stashes
// it for every call whose headers parse, guarded or not, so that a public
// handler that still needs identity of its own (UserService/Register, before
// any account exists) can read it from context instead of re-parsing headers
// — which, several calls deep in a shared helper, it may no longer have
// direct access to at all.
type metaContextKey struct{}

// ClearanceInterceptor resolves the caller from request headers and rejects
// the call when their clearance is below the declared minimum.
//
// On success it stores the resolved user in the context, retrievable with
// CallerFromContext, so handlers do not re-query the same row.
type ClearanceInterceptor struct {
	reqs    Requirements
	resolve CallerResolver
}

// NewClearanceInterceptor builds a ClearanceInterceptor.
func NewClearanceInterceptor(reqs Requirements, resolve CallerResolver) *ClearanceInterceptor {
	return &ClearanceInterceptor{reqs: reqs, resolve: resolve}
}

// WrapUnary implements connect.Interceptor.
func (i *ClearanceInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure

		// Parsed once here regardless of whether the procedure is guarded — see
		// metaContextKey — and reused below instead of parsing the headers a
		// second time for a guarded call.
		meta, metaErr := callermeta.FromHeader(req.Header())
		if metaErr == nil {
			ctx = context.WithValue(ctx, metaContextKey{}, meta)
		}

		minimum, guarded := i.reqs[procedure]
		if !guarded {
			// Public. No caller resolution, and therefore no metadata is required
			// either — a handler that needs identity anyway asks for it itself.
			return next(ctx, req)
		}

		if i.resolve == nil {
			// Wiring bug: a guarded method with no way to resolve anyone. Failing
			// closed beats dereferencing nil in every request.
			log.Z.Error("clearance interceptor has no caller resolver",
				zap.String("procedure", procedure))
			return nil, connect.NewError(connect.CodeInternal, errors.New("caller resolution is not configured"))
		}

		caller, err := i.resolveCaller(ctx, meta, metaErr)
		if err != nil {
			return nil, err
		}

		// Clearance values are non-contiguous (1, 10, 20, 50, 100), so this is a
		// numeric floor and not a step comparison.
		if caller.Clearance < int32(minimum) {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("%s requires %s clearance", procedure, minimum.String()))
		}

		return next(context.WithValue(ctx, callerContextKey{}, caller), req)
	}
}

// WrapStreamingClient is a no-op. Clearance is a server-side concern here:
// wrapping the client half would apply it to outgoing calls this process
// makes, and this server makes none through its own interceptor chain.
func (i *ClearanceInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler enforces the same floor on a streaming RPC.
// OpenClientActionStream is the only streaming RPC, and identity is resolved
// once here, before the handler runs, exactly as on the unary path — a bidi
// stream has one set of request headers for its whole lifetime, sent with the
// opening request, so there is nothing per-message to re-check.
func (i *ClearanceInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		procedure := conn.Spec().Procedure

		meta, metaErr := callermeta.FromHeader(conn.RequestHeader())
		if metaErr == nil {
			ctx = context.WithValue(ctx, metaContextKey{}, meta)
		}

		minimum, guarded := i.reqs[procedure]
		if !guarded {
			return next(ctx, conn)
		}

		if i.resolve == nil {
			log.Z.Error("clearance interceptor has no caller resolver",
				zap.String("procedure", procedure))
			return connect.NewError(connect.CodeInternal, errors.New("caller resolution is not configured"))
		}

		caller, err := i.resolveCaller(ctx, meta, metaErr)
		if err != nil {
			return err
		}

		if caller.Clearance < int32(minimum) {
			return connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("%s requires %s clearance", procedure, minimum.String()))
		}

		return next(context.WithValue(ctx, callerContextKey{}, caller), conn)
	}
}

// resolveCaller turns an already-parsed header identity into the caller's
// user_account row. meta and metaErr are whatever callermeta.FromHeader
// produced for this request; passing them in rather than reparsing keeps the
// header read to exactly once per call.
func (i *ClearanceInterceptor) resolveCaller(ctx context.Context, meta *callermeta.Caller, metaErr error) (*model.User, error) {
	if metaErr != nil {
		return nil, metaErr
	}

	if meta.PlatformUID == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s metadata is required", callermeta.HeaderUserID))
	}

	caller, err := i.resolve(ctx, meta.PlatformEnum, *meta.PlatformUID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("caller is not registered"))
	}
	if err != nil {
		log.Z.Error("failed to resolve caller", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to resolve caller"))
	}
	if caller == nil {
		// A resolver that reports neither a user nor an error would otherwise
		// nil-deref on the clearance check above.
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("caller is not registered"))
	}

	return caller, nil
}

// CallerFromContext returns the caller resolved by the interceptor.
//
// ok is false on a public method, where no caller was resolved.
func CallerFromContext(ctx context.Context) (*model.User, bool) {
	caller, ok := ctx.Value(callerContextKey{}).(*model.User)
	return caller, ok
}

// MetaFromContext returns the caller's raw asserted identity, stashed by
// ClearanceInterceptor for every call whose headers parsed, guarded or not.
//
// ok is false when the headers failed to parse, or no clearance interceptor
// ran at all — which does not happen once ClearanceInterceptor is installed in
// the chain, since it always attempts the parse before checking whether the
// procedure is guarded.
func MetaFromContext(ctx context.Context) (*callermeta.Caller, bool) {
	meta, ok := ctx.Value(metaContextKey{}).(*callermeta.Caller)
	return meta, ok
}
