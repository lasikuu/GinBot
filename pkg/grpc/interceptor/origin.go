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

// originContextKey holds the Origin a call itself came from, stashed by
// OriginInterceptor whenever request headers carried one. Handlers that only
// need to know a call's own origin (trigger scoping, WANHA's repost scope)
// read it via OriginFromContext instead of re-parsing headers, which they no
// longer have direct access to once they are several calls deep in a shared
// helper.
type originContextKey struct{}

// OriginInterceptor creates the instance and destination rows for a guild or
// channel the bot has not seen before, and makes the call's own origin
// available to handlers via OriginFromContext.
//
// It only ever bootstraps for a call whose caller ClearanceInterceptor already
// resolved, so it must be chained after that one.
//
// Bootstrap is best effort. A guild that cannot be recorded must not stop
// someone rolling dice, so a failure is logged and the call proceeds.
type OriginInterceptor struct {
	resolve OriginResolver
	cache   *originCache
}

// NewOriginInterceptor builds an OriginInterceptor.
func NewOriginInterceptor(resolve OriginResolver) *OriginInterceptor {
	return &OriginInterceptor{resolve: resolve, cache: newOriginCache()}
}

// WrapUnary implements connect.Interceptor.
func (i *OriginInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(i.bootstrap(ctx, req.Header()), req)
	}
}

// WrapStreamingClient is a no-op. Origin bootstrap is a server-side concern
// here: wrapping the client half would apply it to outgoing calls this
// process makes, and this server makes none through its own interceptor
// chain.
func (i *OriginInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler runs the same bootstrap the unary path does. It was a
// no-op until TriggerService.GetFile became server-streaming: GetFile's
// visibility check calls callerOriginInstanceID, which reads
// OriginFromContext, so had this stayed a no-op a file visible only through
// the caller's origin instance rather than through ownership would have
// started returning NotFound — a silent authorisation-shaped regression, not a
// missing feature.
//
// For OpenClientActionStream this does nothing, but the reason is a property
// of THIS REPOSITORY'S CLIENTS, not of the interceptor: RunClientActionStream
// attaches caller identity and no origin (pkg/grpc/client/reverse.go), because
// a platform client's stream serves every guild and room that platform is in
// for the life of the connection rather than one instance or channel it was
// "opened from". With no origin headers, callermeta.OriginFromHeader reports
// ok == false and bootstrap returns the context untouched.
//
// A registered caller that set the origin headers on a reverse stream by hand
// WOULD bootstrap a row. That is deliberately not special-cased: it is exactly
// what the same caller can already do on any unary call, originCache bounds
// the repeat inserts identically, and the write requires a caller
// ClearanceInterceptor has already resolved (see bootstrap). Excluding one
// procedure by name here would buy nothing and add a second place where the
// interceptor has to know which RPC it is running on.
func (i *OriginInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(i.bootstrap(ctx, conn.RequestHeader()), conn)
	}
}

// bootstrap is the unary implementation: it stashes the call's own origin into
// the returned context whenever the request headers carried one, and — best
// effort — writes the instance and destination rows for it the first time this
// process sees it.
func (i *OriginInterceptor) bootstrap(ctx context.Context, header http.Header) context.Context {
	// Writing a row is tied to a caller who is authorised and registered.
	// Public methods resolve nobody, so without this an unregistered user
	// typing ??number or /ping in a channel the bot has not seen would insert
	// a destination row — and Discord counts every thread as a channel, so
	// anyone able to create threads could grow that table without limit.
	if _, resolved := CallerFromContext(ctx); !resolved {
		return ctx
	}

	origin, ok := callermeta.OriginFromHeader(header)
	if !ok {
		// Also the normal path for a direct message, which belongs to no guild.
		return ctx
	}
	ctx = context.WithValue(ctx, originContextKey{}, origin)

	if i.resolve == nil {
		return ctx
	}

	// A destination is half of what is being created, so an origin without
	// one is not worth a row.
	if origin.DestinationUID == "" {
		return ctx
	}

	meta, ok := MetaFromContext(ctx)
	if !ok {
		// An origin without a platform cannot be stored. The handler decides
		// whether the missing metadata is fatal for it.
		return ctx
	}

	key := originKey(meta.PlatformEnum, origin)
	if !i.cache.known(key) && bootstrapOrigin(ctx, i.resolve, meta.PlatformEnum, origin) {
		i.cache.remember(key)
	}

	return ctx
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

// OriginFromContext returns the origin a call itself came from, stashed by
// OriginInterceptor.
//
// ok is false when the call carried none — normal for a direct message — or
// when no origin interceptor ran, which does not happen on the unary path once
// OriginInterceptor is installed in the chain.
func OriginFromContext(ctx context.Context) (callermeta.Origin, bool) {
	origin, ok := ctx.Value(originContextKey{}).(callermeta.Origin)
	return origin, ok
}
