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

// Requirements is DEFAULT-OPEN: a procedure absent from the map is public and
// unauthenticated. Keys must be the generated ginbotv1connect.*Procedure
// constants — a hand-written string still compiles and silently opens the RPC.
type Requirements map[string]pb.Clearance

// CallerResolver must report an unknown identity as db.ErrNotFound; any other
// error is treated as a failure to reach the store.
type CallerResolver func(ctx context.Context, platform pb.Platform, platformUID string) (*model.User, error)

type callerContextKey struct{}

// metaContextKey holds the caller's raw asserted identity, so a public handler
// can read it without re-parsing headers.
type metaContextKey struct{}

// ClearanceInterceptor resolves the caller from request headers and rejects the
// call when their clearance is below the declared minimum.
type ClearanceInterceptor struct {
	reqs    Requirements
	resolve CallerResolver
}

func NewClearanceInterceptor(reqs Requirements, resolve CallerResolver) *ClearanceInterceptor {
	return &ClearanceInterceptor{reqs: reqs, resolve: resolve}
}

func (i *ClearanceInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure

		// Parsed even for a public procedure, so MetaFromContext works there.
		meta, metaErr := callermeta.FromHeader(req.Header())
		if metaErr == nil {
			ctx = context.WithValue(ctx, metaContextKey{}, meta)
		}

		minimum, guarded := i.reqs[procedure]
		if !guarded {
			return next(ctx, req)
		}

		if i.resolve == nil {
			// A guarded method with no resolver fails closed.
			log.Z.Error("clearance interceptor has no caller resolver",
				zap.String("procedure", procedure))
			return nil, connect.NewError(connect.CodeInternal, errors.New("caller resolution is not configured"))
		}

		caller, err := i.resolveCaller(ctx, meta, metaErr)
		if err != nil {
			return nil, err
		}

		// Clearance values are non-contiguous: compare numerically, not by ordering.
		if caller.Clearance < int32(minimum) {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("%s requires %s clearance", procedure, minimum.String()))
		}

		return next(context.WithValue(ctx, callerContextKey{}, caller), req)
	}
}

// WrapStreamingClient is a no-op: clearance is server-side only.
func (i *ClearanceInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler enforces the floor once at stream open: a stream carries
// one set of request headers for its whole lifetime.
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
		// A resolver reporting neither a user nor an error would nil-deref below.
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("caller is not registered"))
	}

	return caller, nil
}

// CallerFromContext returns ok false on a public method.
func CallerFromContext(ctx context.Context) (*model.User, bool) {
	caller, ok := ctx.Value(callerContextKey{}).(*model.User)
	return caller, ok
}

// MetaFromContext returns the caller's raw asserted identity; ok is false when
// the headers failed to parse or no ClearanceInterceptor ran.
func MetaFromContext(ctx context.Context) (*callermeta.Caller, bool) {
	meta, ok := ctx.Value(metaContextKey{}).(*callermeta.Caller)
	return meta, ok
}
