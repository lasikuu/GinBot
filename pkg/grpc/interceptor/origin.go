package interceptor

import (
	"context"
	"strings"
	"sync"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// OriginResolver creates the instance and destination rows for a platform
// origin when they do not exist yet, returning the destination id.
//
// It is injected, and its signature is that of db.GetOrCreateDestinationByMeta,
// which does both inserts in one transaction with ON CONFLICT. A read-then-
// insert here would race: two members typing in the same new guild at once
// would both miss the read and both insert.
type OriginResolver func(ctx context.Context, destination *pb.ReminderDestination) (int64, error)

// originCacheMaxEntries bounds the set of origins already bootstrapped.
// Without a bound the set grows with every channel the bot is ever addressed in.
const originCacheMaxEntries = 4096

// originCacheEvictDivisor sets how much of the set is dropped on overflow: one
// entry in this many.
//
// Dropping the whole set instead would make the cache useless above the bound —
// a busy deployment would refill it, hit the limit, and start over, so most
// requests would pay a transaction. Evicting a fraction keeps the bulk of the
// working set and amortises the scan over the entries it frees. The victims are
// arbitrary rather than least-recently-used, which is fine: the only cost of
// forgetting an origin is one redundant upsert, and that does not warrant
// carrying an LRU.
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

// remember records a successful bootstrap. Only success is recorded, so a
// failed one is retried on the next call from the same origin.
func (c *originCache) remember(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.seen) >= originCacheMaxEntries {
		c.evictLocked()
	}
	c.seen[key] = struct{}{}
}

// evictLocked frees room by dropping a fraction of the set. Go randomises map
// iteration order, so which origins go is arbitrary.
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

// NewOriginUnaryInterceptor creates the instance and destination rows for a
// guild or channel the bot has not seen before.
//
// It only ever runs for a call whose caller the clearance interceptor resolved,
// so it must be chained inside that one.
//
// Bootstrap is best effort. A guild that cannot be recorded must not stop
// someone rolling dice, so a failure is logged and the call proceeds.
func NewOriginUnaryInterceptor(resolve OriginResolver) grpc.UnaryServerInterceptor {
	cache := newOriginCache()

	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if resolve == nil {
			return handler(ctx, req)
		}

		// Writing a row is tied to a caller who is authorised and registered.
		// Public methods resolve nobody, so without this an unregistered user
		// typing ??number or /ping in a channel the bot has not seen would insert
		// a destination row — and Discord counts every thread as a channel, so
		// anyone able to create threads could grow that table without limit.
		if _, resolved := CallerFromContext(ctx); !resolved {
			return handler(ctx, req)
		}

		origin, ok := callermeta.OriginFromIncomingContext(ctx)
		// A destination is half of what is being created, so an origin without
		// one is not worth a row.
		if !ok || origin.DestinationUID == "" {
			// Also the normal path for a direct message, which belongs to no guild.
			return handler(ctx, req)
		}

		meta, err := callermeta.FromIncomingContext(ctx)
		if err != nil {
			// An origin without a platform cannot be stored. The handler decides
			// whether the missing metadata is fatal for it.
			return handler(ctx, req)
		}

		key := originKey(meta.PlatformEnum, origin)
		if !cache.known(key) && bootstrapOrigin(ctx, resolve, meta.PlatformEnum, origin) {
			cache.remember(key)
		}

		return handler(ctx, req)
	}
}

// originKey identifies one origin on one platform. NUL separates the parts
// because it cannot occur in a platform enum name or a platform identifier, so
// two different origins cannot collide by running together.
func originKey(platform pb.Platform, origin callermeta.Origin) string {
	return strings.Join([]string{platform.String(), origin.InstanceUID, origin.DestinationUID}, "\x00")
}

// bootstrapOrigin writes the instance and destination rows, reporting success.
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
