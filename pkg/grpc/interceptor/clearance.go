package interceptor

import (
	"context"
	"errors"

	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Requirements maps a full RPC method name to the minimum clearance required.
// Methods absent from the map are public: no caller is resolved for them.
//
// Keys must be the generated *_FullMethodName constants. A hand-written string
// that drifts from the generated one still compiles and silently makes the RPC
// public, which is a security failure rather than a bug.
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

// NewClearanceUnaryInterceptor resolves the caller from metadata and rejects the
// call when their clearance is below the declared minimum.
//
// On success it stores the resolved user in the context, retrievable with
// CallerFromContext, so handlers do not re-query the same row.
func NewClearanceUnaryInterceptor(reqs Requirements, resolve CallerResolver) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		minimum, guarded := reqs[info.FullMethod]
		if !guarded {
			// Public. No caller resolution, and therefore no metadata is required
			// either — a handler that needs identity anyway asks for it itself.
			return handler(ctx, req)
		}

		if resolve == nil {
			// Wiring bug: a guarded method with no way to resolve anyone. Failing
			// closed beats dereferencing nil in every request.
			log.Z.Error("clearance interceptor has no caller resolver",
				zap.String("method", info.FullMethod))
			return nil, status.Errorf(codes.Internal, "caller resolution is not configured")
		}

		caller, err := resolveCaller(ctx, resolve)
		if err != nil {
			return nil, err
		}

		// Clearance values are non-contiguous (1, 10, 20, 50, 100), so this is a
		// numeric floor and not a step comparison.
		if caller.Clearance < int32(minimum) {
			return nil, status.Errorf(codes.PermissionDenied,
				"%s requires %s clearance", info.FullMethod, minimum.String())
		}

		return handler(context.WithValue(ctx, callerContextKey{}, caller), req)
	}
}

// resolveCaller turns incoming metadata into the caller's user_account row.
func resolveCaller(ctx context.Context, resolve CallerResolver) (*model.User, error) {
	meta, err := callermeta.FromIncomingContext(ctx)
	if err != nil {
		return nil, err
	}

	if meta.PlatformUID == nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s metadata is required", callermeta.HeaderUserID)
	}

	caller, err := resolve(ctx, meta.PlatformEnum, *meta.PlatformUID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, status.Errorf(codes.FailedPrecondition, "caller is not registered")
	}
	if err != nil {
		log.Z.Error("failed to resolve caller", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to resolve caller")
	}
	if caller == nil {
		// A resolver that reports neither a user nor an error would otherwise
		// nil-deref on the clearance check below.
		return nil, status.Errorf(codes.FailedPrecondition, "caller is not registered")
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
