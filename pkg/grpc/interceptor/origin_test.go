package interceptor

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
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

// originTestCtx builds the context OriginInterceptor actually sees in
// production: a resolved caller (stashed by ClearanceInterceptor) AND the
// caller's raw asserted identity under metaContextKey (also stashed by
// ClearanceInterceptor, for every call whose headers parsed — see
// clearance.go). bootstrap() reads the platform through MetaFromContext, not
// by re-parsing headers itself, so a standalone OriginInterceptor test has to
// populate that key by hand rather than relying on ClearanceInterceptor
// having run first, which none of these tests install it to do.
func originTestCtx(header http.Header, caller *model.User) context.Context {
	ctx := context.Background()
	if caller != nil {
		ctx = context.WithValue(ctx, callerContextKey{}, caller)
	}
	if meta, err := callermeta.FromHeader(header); err == nil {
		ctx = context.WithValue(ctx, metaContextKey{}, meta)
	}
	return ctx
}

// callOrigin runs one request through the origin interceptor, carrying header.
func callOrigin(header http.Header, resolve OriginResolver, caller *model.User) originResult {
	var result originResult

	handler := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		result.reached = true
		return newFakeResponse(), nil
	})

	req := newFakeRequest(publicMethod)
	maps.Copy(req.Header(), header)

	intercept := NewOriginInterceptor(resolve)
	_, result.err = intercept.WrapUnary(handler)(originTestCtx(header, caller), req)

	return result
}

// originHeader builds the header a platform client produces: identity and
// origin, both via callermeta so the test cannot disagree with the production
// encoding.
func originHeader(platform pb.Platform, platformUID string, origin callermeta.Origin) http.Header {
	header := wellFormedHeader(platform, platformUID)
	if origin.InstanceUID != "" {
		header.Set(callermeta.HeaderInstanceUID, origin.InstanceUID)
	}
	if origin.DestinationUID != "" {
		header.Set(callermeta.HeaderDestinationUID, origin.DestinationUID)
	}
	return header
}

// testOrigin is a Discord guild and channel.
func testOrigin() callermeta.Origin {
	return callermeta.Origin{InstanceUID: "guild-1", DestinationUID: "channel-1"}
}

// A call with no origin header has nothing to bootstrap. A direct message is
// the normal case.
func TestOriginIsNotBootstrappedWithoutOriginHeader(t *testing.T) {
	tests := []struct {
		name   string
		origin callermeta.Origin
	}{
		{"no origin at all", callermeta.Origin{}},
		// A real client drops an origin with no instance, so this never
		// reaches the wire; a direct message produces exactly this.
		{"destination but no instance", callermeta.Origin{DestinationUID: "dm-channel"}},
		// Half an origin is not worth a row: destination is what would be created.
		{"instance but no destination", callermeta.Origin{InstanceUID: "guild-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeOriginResolver{}
			header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", tt.origin)
			caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))

			got := callOrigin(header, resolver.resolve, caller)

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

	// Full, well-formed identity and origin headers — the only thing missing
	// is the caller the clearance interceptor would have put in the context.
	header := originHeader(pb.Platform_PLATFORM_DISCORD, "stranger", testOrigin())

	got := callOrigin(header, resolver.resolve, nil)

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
	header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", testOrigin())
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))

	got := callOrigin(header, resolver.resolve, caller)

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
	header := originHeader(pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org", origin)
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))

	if got := callOrigin(header, resolver.resolve, caller); got.err != nil {
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
//
// One interceptor instance is reused across every call, not rebuilt through
// callOrigin each time: the cache this test is about lives ON the
// interceptor, and a fresh one per call would trivially pass regardless of
// whether the cache works at all.
func TestBootstrapRunsOncePerOrigin(t *testing.T) {
	resolver := &fakeOriginResolver{}
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))
	intercept := NewOriginInterceptor(resolver.resolve)

	handler := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return newFakeResponse(), nil
	})

	const requests = 5
	for range requests {
		req := newFakeRequest(publicMethod)
		header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", testOrigin())
		maps.Copy(req.Header(), header)
		if _, err := intercept.WrapUnary(handler)(originTestCtx(header, caller), req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if n := resolver.callCount(); n != 1 {
		t.Errorf("resolver ran %d times across %d requests from one origin, want 1", n, requests)
	}
}

// The cache key carries the platform and both identifiers, so origins that
// differ in any of them are bootstrapped separately.
//
// Every request runs through the SAME interceptor instance rather than a
// fresh one per call, because the cache the property depends on lives on the
// interceptor.
func TestBootstrapDistinguishesOrigins(t *testing.T) {
	resolver := &fakeOriginResolver{}
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))
	intercept := NewOriginInterceptor(resolver.resolve)

	handler := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return newFakeResponse(), nil
	})

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

	for _, tt := range calls {
		req := newFakeRequest(publicMethod)
		header := originHeader(tt.platform, "uid", tt.origin)
		maps.Copy(req.Header(), header)

		if _, err := intercept.WrapUnary(handler)(originTestCtx(header, caller), req); err != nil {
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
	intercept := NewOriginInterceptor(resolver.resolve)

	handler := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return newFakeResponse(), nil
	})

	const concurrency = 32
	// Requests are built up front: header construction is not what is under
	// test here, and t.Fatal from a goroutine is not allowed.
	reqs := make([]*fakeRequest, concurrency)
	ctxs := make([]context.Context, concurrency)
	for i := range reqs {
		reqs[i] = newFakeRequest(publicMethod)
		header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", testOrigin())
		maps.Copy(reqs[i].Header(), header)
		ctxs[i] = originTestCtx(header, caller)
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
			_, errs[i] = intercept.WrapUnary(handler)(ctxs[i], reqs[i])
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
		req := newFakeRequest(publicMethod)
		header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", testOrigin())
		maps.Copy(req.Header(), header)
		// originTestCtx, not a bare callerContextKey: bootstrap reads the
		// platform through MetaFromContext and returns before it ever consults
		// the cache when that is absent. A context carrying only the caller
		// makes the resolver unreachable for a reason that has nothing to do
		// with the cache, so this loop would report convergence even with
		// caching removed entirely.
		ctx := originTestCtx(header, caller)
		if _, err := intercept.WrapUnary(handler)(ctx, req); err != nil {
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
	intercept := NewOriginInterceptor(resolver.resolve)

	handler := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return newFakeResponse(), nil
	})

	call := func() error {
		req := newFakeRequest(publicMethod)
		header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", testOrigin())
		maps.Copy(req.Header(), header)
		_, err := intercept.WrapUnary(handler)(originTestCtx(header, caller), req)
		return err
	}

	const attempts = 3
	for i := range attempts {
		// Bootstrap is best effort: a guild that cannot be recorded must not
		// stop someone rolling dice.
		if err := call(); err != nil {
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
		if err := call(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if n := resolver.callCount(); n != attempts+1 {
		t.Errorf("resolver ran %d times, want %d: success was not cached", n, attempts+1)
	}
}

// A server built without an origin resolver must still serve. This is the
// wiring guard: NewOriginInterceptor(nil) is a pass-through, not a panic.
func TestNilResolverIsAPassThrough(t *testing.T) {
	header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", testOrigin())
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))

	got := callOrigin(header, nil, caller)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached with no resolver configured")
	}
}

// A caller in the context but no platform header cannot be stored: the
// platform is half the instance key. It must not fail the call either.
func TestBootstrapIsSkippedWithoutPlatformHeader(t *testing.T) {
	resolver := &fakeOriginResolver{}

	// Origin headers with no platform_enum — something no real client sends,
	// but the interceptor must not assume that.
	origin := testOrigin()
	header := make(http.Header)
	header.Set(callermeta.HeaderInstanceUID, origin.InstanceUID)
	header.Set(callermeta.HeaderDestinationUID, origin.DestinationUID)
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))

	got := callOrigin(header, resolver.resolve, caller)

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
