package interceptor

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeOriginResolver is an OriginResolver that records what it was asked to
// create. It stands in for db.GetOrCreateDestinationByMeta, whose only job is
// the write these tests are asserting does or does not happen.
type fakeOriginResolver struct {
	// err, when set, makes every call fail. Bootstrap is best effort, so this
	// must not fail the RPC.
	err error

	mu           sync.Mutex
	calls        int
	destinations []*pb.ReminderDestination
}

func (f *fakeOriginResolver) resolve(_ context.Context, destination *pb.ReminderDestination) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	f.destinations = append(f.destinations, destination)

	if f.err != nil {
		return 0, f.err
	}

	return int64(f.calls), nil
}

func (f *fakeOriginResolver) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// lastDestination returns what the resolver was most recently handed.
func (f *fakeOriginResolver) lastDestination(t *testing.T) *pb.ReminderDestination {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.destinations) == 0 {
		t.Fatal("resolver was never called")
	}

	return f.destinations[len(f.destinations)-1]
}

// originResult is everything one trip through the origin interceptor produced.
type originResult struct {
	reached bool
	err     error
}

// callOrigin runs one request through the origin interceptor.
func callOrigin(ctx context.Context, resolve OriginResolver) originResult {
	var result originResult

	handler := func(context.Context, any) (any, error) {
		result.reached = true
		return "handled", nil
	}

	intercept := NewOriginUnaryInterceptor(resolve)
	_, result.err = intercept(ctx, struct{}{},
		&grpc.UnaryServerInfo{FullMethod: publicMethod}, handler)

	return result
}

// originCtx builds the incoming context a platform client produces: identity
// and origin, round-tripped through callermeta so the test cannot disagree with
// the production encoding.
func originCtx(t *testing.T, platform pb.Platform, platformUID string, origin callermeta.Origin) context.Context {
	t.Helper()

	ctx := callermeta.NewOutgoingContext(context.Background(), platform, platformUID)
	// After NewOutgoingContext, never before: see TestOriginIsLostWhenAttachedBeforeIdentity.
	ctx = callermeta.NewOutgoingOrigin(ctx, origin)

	return incomingFromOutgoing(t, ctx)
}

// incomingFromOutgoing turns a client's outgoing context into the incoming one
// the server sees.
func incomingFromOutgoing(t *testing.T, ctx context.Context) context.Context {
	t.Helper()

	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("no outgoing metadata attached")
	}

	return metadata.NewIncomingContext(context.Background(), md)
}

// withCaller puts a resolved caller in the context, which is what the clearance
// interceptor does for a guarded method.
func withCaller(ctx context.Context, caller *model.User) context.Context {
	return context.WithValue(ctx, callerContextKey{}, caller)
}

// testOrigin is a Discord guild and channel.
func testOrigin() callermeta.Origin {
	return callermeta.Origin{InstanceUID: "guild-1", DestinationUID: "channel-1"}
}

// A call with no origin metadata has nothing to bootstrap. A direct message is
// the normal case.
func TestOriginIsNotBootstrappedWithoutOriginMetadata(t *testing.T) {
	tests := []struct {
		name   string
		origin callermeta.Origin
	}{
		{"no origin at all", callermeta.Origin{}},
		// callermeta drops an origin with no instance, so this never reaches the
		// wire; a direct message produces exactly this.
		{"destination but no instance", callermeta.Origin{DestinationUID: "dm-channel"}},
		// Half an origin is not worth a row: destination is what would be created.
		{"instance but no destination", callermeta.Origin{InstanceUID: "guild-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeOriginResolver{}
			ctx := withCaller(
				originCtx(t, pb.Platform_PLATFORM_DISCORD, "uid", tt.origin),
				callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED)),
			)

			got := callOrigin(ctx, resolver.resolve)

			if got.err != nil {
				t.Fatalf("unexpected error: %v", got.err)
			}
			if !got.reached {
				t.Error("handler was not reached")
			}
			if n := resolver.callCount(); n != 0 {
				t.Errorf("resolver ran %d times without a usable origin, want 0", n)
			}
		})
	}
}

// The regression test for the finding: a public method resolves no caller, so
// an unregistered stranger typing ??number or /ping in a channel the bot has
// never seen must not cause a write. Discord counts every thread as a channel,
// so this was unbounded growth driven by anyone who could create one.
func TestOriginIsNotBootstrappedWithoutAResolvedCaller(t *testing.T) {
	resolver := &fakeOriginResolver{}

	// Full, well-formed identity and origin metadata — the only thing missing is
	// the caller the clearance interceptor would have put in the context.
	ctx := originCtx(t, pb.Platform_PLATFORM_DISCORD, "stranger", testOrigin())

	got := callOrigin(ctx, resolver.resolve)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached; bootstrap must never block a public call")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times for an unauthenticated caller, want 0", n)
	}
}

func TestOriginIsBootstrappedForAResolvedCaller(t *testing.T) {
	resolver := &fakeOriginResolver{}
	ctx := withCaller(
		originCtx(t, pb.Platform_PLATFORM_DISCORD, "uid", testOrigin()),
		callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED)),
	)

	got := callOrigin(ctx, resolver.resolve)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached")
	}
	if n := resolver.callCount(); n != 1 {
		t.Fatalf("resolver ran %d times, want exactly 1", n)
	}
}

// The row is written from what the resolver is handed, and the jsonb shapes are
// matched by equality against rows already stored. A different shape here
// creates a second instance for a guild the bot already knows, and nothing
// reports the split.
func TestBootstrapPassesTheCanonicalMetaShapes(t *testing.T) {
	resolver := &fakeOriginResolver{}
	origin := testOrigin()
	ctx := withCaller(
		originCtx(t, pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org", origin),
		callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED)),
	)

	if got := callOrigin(ctx, resolver.resolve); got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}

	destination := resolver.lastDestination(t)

	if got := destination.GetPlatformEnum(); got != pb.Platform_PLATFORM_MATRIX_PROTOCOL {
		t.Errorf("platform_enum = %v, want %v", got, pb.Platform_PLATFORM_MATRIX_PROTOCOL)
	}

	tests := []struct {
		name  string
		meta  *structpb.Struct
		field string
		want  string
	}{
		{"instance_meta", destination.GetInstanceMeta(), callermeta.FieldInstanceUID, origin.InstanceUID},
		{"destination_meta", destination.GetDestinationMeta(), callermeta.FieldDestinationUID, origin.DestinationUID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.meta == nil {
				t.Fatalf("%s was nil", tt.name)
			}
			if got := len(tt.meta.GetFields()); got != 1 {
				t.Errorf("%s has %d fields, want exactly 1", tt.name, got)
			}
			value, ok := tt.meta.GetFields()[tt.field]
			if !ok {
				t.Fatalf("%s has no %q field, got %v", tt.name, tt.field, tt.meta.GetFields())
			}
			if got := value.GetStringValue(); got != tt.want {
				t.Errorf("%s[%s] = %q, want %q", tt.name, tt.field, got, tt.want)
			}
		})
	}
}

// The upsert is meant to run on first contact, not on every request. Without
// the cache every message in a busy channel would cost a transaction.
func TestBootstrapRunsOncePerOrigin(t *testing.T) {
	resolver := &fakeOriginResolver{}
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))
	intercept := NewOriginUnaryInterceptor(resolver.resolve)

	handler := func(context.Context, any) (any, error) { return "handled", nil }
	info := &grpc.UnaryServerInfo{FullMethod: publicMethod}

	const requests = 5
	for range requests {
		ctx := withCaller(originCtx(t, pb.Platform_PLATFORM_DISCORD, "uid", testOrigin()), caller)
		if _, err := intercept(ctx, struct{}{}, info, handler); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if n := resolver.callCount(); n != 1 {
		t.Errorf("resolver ran %d times across %d requests from one origin, want 1", n, requests)
	}
}

// The cache key carries the platform and both identifiers, so origins that
// differ in any of them are bootstrapped separately.
func TestBootstrapDistinguishesOrigins(t *testing.T) {
	resolver := &fakeOriginResolver{}
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))
	intercept := NewOriginUnaryInterceptor(resolver.resolve)

	handler := func(context.Context, any) (any, error) { return "handled", nil }
	info := &grpc.UnaryServerInfo{FullMethod: publicMethod}

	calls := []struct {
		platform pb.Platform
		origin   callermeta.Origin
	}{
		{pb.Platform_PLATFORM_DISCORD, callermeta.Origin{InstanceUID: "g1", DestinationUID: "c1"}},
		// Same guild, different channel.
		{pb.Platform_PLATFORM_DISCORD, callermeta.Origin{InstanceUID: "g1", DestinationUID: "c2"}},
		// Different guild, same channel id.
		{pb.Platform_PLATFORM_DISCORD, callermeta.Origin{InstanceUID: "g2", DestinationUID: "c1"}},
		// Identical identifiers on another platform are a different place.
		{pb.Platform_PLATFORM_MATRIX_PROTOCOL, callermeta.Origin{InstanceUID: "g1", DestinationUID: "c1"}},
		// A repeat of the first, which must not add a call.
		{pb.Platform_PLATFORM_DISCORD, callermeta.Origin{InstanceUID: "g1", DestinationUID: "c1"}},
	}

	for _, call := range calls {
		ctx := withCaller(originCtx(t, call.platform, "uid", call.origin), caller)
		if _, err := intercept(ctx, struct{}{}, info, handler); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if n := resolver.callCount(); n != 4 {
		t.Errorf("resolver ran %d times, want 4 distinct origins", n)
	}
}

// First contact is exactly when several members are likely to be talking at
// once. Run under -race, this is the check that the cache is safe to share.
func TestConcurrentFirstContactConvergesOnOneBootstrap(t *testing.T) {
	resolver := &fakeOriginResolver{}
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))
	intercept := NewOriginUnaryInterceptor(resolver.resolve)

	handler := func(context.Context, any) (any, error) { return "handled", nil }
	info := &grpc.UnaryServerInfo{FullMethod: publicMethod}

	const concurrency = 32
	// Contexts are built up front: callermeta is not what is under test here,
	// and t.Fatal from a goroutine is not allowed.
	contexts := make([]context.Context, concurrency)
	for i := range contexts {
		contexts[i] = withCaller(originCtx(t, pb.Platform_PLATFORM_DISCORD, "uid", testOrigin()), caller)
	}

	errs := make([]error, concurrency)

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := range concurrency {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait() // release together to maximise contention
			_, errs[i] = intercept(contexts[i], struct{}{}, info, handler)
		}(i)
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	// The cache is not held across the resolver call, so a simultaneous burst
	// can legitimately produce more than one upsert — every goroutine may miss
	// the read before any of them records a success. The upsert is idempotent,
	// so that is acceptable, and pinning an exact count here would only make the
	// test flaky.
	burst := resolver.callCount()
	if burst < 1 {
		t.Fatalf("resolver never ran for %d concurrent first-contact requests", concurrency)
	}
	if burst > concurrency {
		t.Fatalf("resolver ran %d times for %d requests", burst, concurrency)
	}

	// Convergence is the property that matters and it is deterministic: once the
	// burst is over the origin is known, so no later request bootstraps again.
	for range concurrency {
		ctx := withCaller(originCtx(t, pb.Platform_PLATFORM_DISCORD, "uid", testOrigin()), caller)
		if _, err := intercept(ctx, struct{}{}, info, handler); err != nil {
			t.Fatalf("post-burst request failed: %v", err)
		}
	}

	if n := resolver.callCount(); n != burst {
		t.Errorf("resolver ran %d more times after the burst, want 0: the origin was not remembered", n-burst)
	}
}

// Only success is cached, so a transient database failure must not permanently
// stop an origin being recorded.
func TestFailedBootstrapIsRetriedAndDoesNotFailTheCall(t *testing.T) {
	resolver := &fakeOriginResolver{err: errors.New("dial tcp: connection refused")}
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))
	intercept := NewOriginUnaryInterceptor(resolver.resolve)

	handler := func(context.Context, any) (any, error) { return "handled", nil }
	info := &grpc.UnaryServerInfo{FullMethod: publicMethod}

	const attempts = 3
	for i := range attempts {
		ctx := withCaller(originCtx(t, pb.Platform_PLATFORM_DISCORD, "uid", testOrigin()), caller)
		// Bootstrap is best effort: a guild that cannot be recorded must not stop
		// someone rolling dice.
		if _, err := intercept(ctx, struct{}{}, info, handler); err != nil {
			t.Fatalf("attempt %d failed the RPC: %v", i, err)
		}
	}

	if n := resolver.callCount(); n != attempts {
		t.Errorf("resolver ran %d times, want %d: a failure must not be cached", n, attempts)
	}

	// And once it succeeds, it is cached like any other success.
	resolver.mu.Lock()
	resolver.err = nil
	resolver.mu.Unlock()

	for range 2 {
		ctx := withCaller(originCtx(t, pb.Platform_PLATFORM_DISCORD, "uid", testOrigin()), caller)
		if _, err := intercept(ctx, struct{}{}, info, handler); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if n := resolver.callCount(); n != attempts+1 {
		t.Errorf("resolver ran %d times, want %d: success was not cached", n, attempts+1)
	}
}

// A server built without an origin resolver must still serve. This is the
// wiring guard: NewOriginUnaryInterceptor(nil) is a pass-through, not a panic.
func TestNilResolverIsAPassThrough(t *testing.T) {
	ctx := withCaller(
		originCtx(t, pb.Platform_PLATFORM_DISCORD, "uid", testOrigin()),
		callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED)),
	)

	got := callOrigin(ctx, nil)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached with no resolver configured")
	}
}

// A caller in the context but no platform metadata cannot be stored: the
// platform is half the instance key. It must not fail the call either.
func TestBootstrapIsSkippedWithoutPlatformMetadata(t *testing.T) {
	resolver := &fakeOriginResolver{}

	// Origin headers with no platform_enum — something no real client sends, but
	// the interceptor must not assume that.
	origin := testOrigin()
	ctx := withCaller(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			callermeta.HeaderInstanceUID, origin.InstanceUID,
			callermeta.HeaderDestinationUID, origin.DestinationUID,
		)),
		callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED)),
	)

	got := callOrigin(ctx, resolver.resolve)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times with no platform, want 0", n)
	}
}

// originCacheMaxEntries is a bound, not a cliff: above it the cache must keep
// most of its contents rather than emptying and starting over.
func TestOriginCacheEvictsAFractionOnOverflow(t *testing.T) {
	cache := newOriginCache()

	for i := range originCacheMaxEntries {
		cache.remember(originKeyForTest(i))
	}
	if got := len(cache.seen); got != originCacheMaxEntries {
		t.Fatalf("cache holds %d entries, want %d before overflow", got, originCacheMaxEntries)
	}

	// One more entry triggers eviction.
	cache.remember(originKeyForTest(originCacheMaxEntries))

	// A whole-map flush would leave 1. A bounded eviction leaves the rest.
	want := originCacheMaxEntries - originCacheMaxEntries/originCacheEvictDivisor + 1
	if got := len(cache.seen); got != want {
		t.Errorf("cache holds %d entries after overflow, want %d", got, want)
	}
	if !cache.known(originKeyForTest(originCacheMaxEntries)) {
		t.Error("the entry that triggered eviction was not remembered")
	}
}

// originKeyForTest builds a distinct cache key. The real keys come from
// originKey, which is exercised by TestBootstrapDistinguishesOrigins.
func originKeyForTest(i int) string {
	return originKey(pb.Platform_PLATFORM_DISCORD, callermeta.Origin{
		InstanceUID:    "guild",
		DestinationUID: string(rune('a'+i%26)) + string(rune('a'+i/26)),
	})
}
