package interceptor

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// OriginResolver creates the instance and destination rows for a platform
// origin when they do not exist yet, returning the destination id. It must
// upsert in one transaction: concurrent first calls from one guild race.
type OriginResolver func(ctx context.Context, destination *pb.ReminderDestination) (int64, error)

// originCacheMaxEntries bounds the set of origins already bootstrapped.
const originCacheMaxEntries = 4096

// originCacheEvictDivisor drops one entry in this many on overflow; dropping
// the whole set would make the cache useless above the bound.
const originCacheEvictDivisor = 4

// originCache remembers which origins have already been written, so that the
// upsert runs on first contact instead of on every request.
type originCache struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newOriginCache() *originCache {
	return &originCache{seen: make(map[string]struct{})}
}

func (c *originCache) known(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.seen[key]
	return ok
}

// remember records only successful bootstraps, so a failure is retried.
func (c *originCache) remember(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.seen) >= originCacheMaxEntries {
		c.evictLocked()
	}
	c.seen[key] = struct{}{}
}

// evictLocked drops a fraction of the set; map order makes the victims arbitrary.
func (c *originCache) evictLocked() {
	remaining := len(c.seen) / originCacheEvictDivisor
	if remaining == 0 {
		remaining = 1
	}

	for key := range c.seen {
		delete(c.seen, key)
		remaining--
		if remaining == 0 {
			return
		}
	}
}

type originContextKey struct{}

// OriginInterceptor creates the instance and destination rows for a guild or
// channel the bot has not seen before, and exposes the call's own origin via
// OriginFromContext. Must be chained after ClearanceInterceptor; best effort.
type OriginInterceptor struct {
	resolve OriginResolver
	cache   *originCache
}

func NewOriginInterceptor(resolve OriginResolver) *OriginInterceptor {
	return &OriginInterceptor{resolve: resolve, cache: newOriginCache()}
}

func (i *OriginInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(i.bootstrap(ctx, req.Header()), req)
	}
}

// WrapStreamingClient is a no-op: origin bootstrap is server-side only.
func (i *OriginInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler runs the same bootstrap the unary path does; streaming
// GetFile's visibility check reads OriginFromContext.
func (i *OriginInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(i.bootstrap(ctx, conn.RequestHeader()), conn)
	}
}

func (i *OriginInterceptor) bootstrap(ctx context.Context, header http.Header) context.Context {
	// Writing a row requires a resolved caller: otherwise anyone able to create
	// a Discord thread could grow the destination table without limit.
	if _, resolved := CallerFromContext(ctx); !resolved {
		return ctx
	}

	origin, ok := callermeta.OriginFromHeader(header)
	if !ok {
		// The normal path for a direct message, which belongs to no guild.
		return ctx
	}
	ctx = context.WithValue(ctx, originContextKey{}, origin)

	if i.resolve == nil {
		return ctx
	}

	if origin.DestinationUID == "" {
		return ctx
	}

	meta, ok := MetaFromContext(ctx)
	if !ok {
		// An origin without a platform cannot be stored.
		return ctx
	}

	key := originKey(meta.PlatformEnum, origin)
	if !i.cache.known(key) && bootstrapOrigin(ctx, i.resolve, meta.PlatformEnum, origin) {
		i.cache.remember(key)
	}

	return ctx
}

// originKey joins on NUL, which cannot occur in any part, so two different
// origins cannot collide by running together.
func originKey(platform pb.Platform, origin callermeta.Origin) string {
	return strings.Join([]string{platform.String(), origin.InstanceUID, origin.DestinationUID}, "\x00")
}

func bootstrapOrigin(ctx context.Context, resolve OriginResolver, platform pb.Platform, origin callermeta.Origin) bool {
	destination := pb.ReminderDestination_builder{
		PlatformEnum:    platform.Enum(),
		InstanceMeta:    origin.InstanceMeta(),
		DestinationMeta: origin.DestinationMeta(),
	}.Build()

	destinationID, err := resolve(ctx, destination)
	if err != nil {
		log.Z.Warn("failed to bootstrap origin",
			zap.String("platform", platform.String()),
			zap.String("instance_uid", origin.InstanceUID),
			zap.String("destination_uid", origin.DestinationUID),
			zap.Error(err),
		)
		return false
	}

	log.Z.Info("bootstrapped origin",
		zap.String("platform", platform.String()),
		zap.String("instance_uid", origin.InstanceUID),
		zap.Int64("destination_id", destinationID),
	)

	return true
}

// OriginFromContext returns ok false when the call carried no origin, which is
// normal for a direct message.
func OriginFromContext(ctx context.Context) (callermeta.Origin, bool) {
	origin, ok := ctx.Value(originContextKey{}).(callermeta.Origin)
	return origin, ok
}
