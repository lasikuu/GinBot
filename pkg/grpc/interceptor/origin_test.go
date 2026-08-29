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

// fakeOriginResolver records what it was asked to create.
type fakeOriginResolver struct {
	// err, when set, makes every call fail.
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

func (f *fakeOriginResolver) lastDestination(t *testing.T) *pb.ReminderDestination {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.destinations) == 0 {
		t.Fatal("resolver was never called")
	}

	return f.destinations[len(f.destinations)-1]
}

type originResult struct {
	reached bool
	err     error
}

// originTestCtx builds the context ClearanceInterceptor would have produced:
// bootstrap reads the platform through MetaFromContext, not from the headers.
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

func testOrigin() callermeta.Origin {
	return callermeta.Origin{InstanceUID: "guild-1", DestinationUID: "channel-1"}
}

func TestOriginIsNotBootstrappedWithoutOriginHeader(t *testing.T) {
	tests := []struct {
		name   string
		origin callermeta.Origin
	}{
		{"no origin at all", callermeta.Origin{}},
		{"destination but no instance", callermeta.Origin{DestinationUID: "dm-channel"}},
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

// An unresolved caller must not cause a write: Discord counts every thread as a
// channel, so anyone able to create one could grow the table without limit.
func TestOriginIsNotBootstrappedWithoutAResolvedCaller(t *testing.T) {
	resolver := &fakeOriginResolver{}

	// Well-formed headers; only the resolved caller is missing.
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

// The jsonb shapes are matched by equality against rows already stored.
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

// One interceptor instance across every call: the cache lives on it, so a fresh
// one per call would pass regardless of whether the cache works.
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
		{pb.Platform_PLATFORM_DISCORD, callermeta.Origin{InstanceUID: "g1", DestinationUID: "c2"}},
		{pb.Platform_PLATFORM_DISCORD, callermeta.Origin{InstanceUID: "g2", DestinationUID: "c1"}},
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

// Under -race, this is the check that the cache is safe to share.
func TestConcurrentFirstContactConvergesOnOneBootstrap(t *testing.T) {
	resolver := &fakeOriginResolver{}
	caller := callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))
	intercept := NewOriginInterceptor(resolver.resolve)

	handler := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return newFakeResponse(), nil
	})

	const concurrency = 32
	// Built up front: t.Fatal from a goroutine is not allowed.
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

	// The cache is not held across the resolver call, so a burst can produce
	// more than one upsert; the upsert is idempotent, so an exact count is flaky.
	burst := resolver.callCount()
	if burst < 1 {
		t.Fatalf("resolver never ran for %d concurrent first-contact requests", concurrency)
	}
	if burst > concurrency {
		t.Fatalf("resolver ran %d times for %d requests", burst, concurrency)
	}

	// Once the burst is over the origin is known, so nothing bootstraps again.
	for range concurrency {
		req := newFakeRequest(publicMethod)
		header := originHeader(pb.Platform_PLATFORM_DISCORD, "uid", testOrigin())
		maps.Copy(req.Header(), header)
		// originTestCtx, not a bare callerContextKey: without the meta value
		// bootstrap returns before consulting the cache and this passes vacuously.
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
		// Bootstrap is best effort and must not fail the RPC.
		if err := call(); err != nil {
			t.Fatalf("attempt %d failed the RPC: %v", i, err)
		}
	}

	if n := resolver.callCount(); n != attempts {
		t.Errorf("resolver ran %d times, want %d: a failure must not be cached", n, attempts)
	}

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

// NewOriginInterceptor(nil) must be a pass-through, not a panic.
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

// The platform is half the instance key, so an origin without one is unstorable.
func TestBootstrapIsSkippedWithoutPlatformHeader(t *testing.T) {
	resolver := &fakeOriginResolver{}

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

// Above the bound the cache must keep most of its contents, not empty itself.
func TestOriginCacheEvictsAFractionOnOverflow(t *testing.T) {
	cache := newOriginCache()

	for i := range originCacheMaxEntries {
		cache.remember(originKeyForTest(i))
	}
	if got := len(cache.seen); got != originCacheMaxEntries {
		t.Fatalf("cache holds %d entries, want %d before overflow", got, originCacheMaxEntries)
	}

	// One more entry triggers eviction; a whole-map flush would leave 1.
	cache.remember(originKeyForTest(originCacheMaxEntries))

	want := originCacheMaxEntries - originCacheMaxEntries/originCacheEvictDivisor + 1
	if got := len(cache.seen); got != want {
		t.Errorf("cache holds %d entries after overflow, want %d", got, want)
	}
	if !cache.known(originKeyForTest(originCacheMaxEntries)) {
		t.Error("the entry that triggered eviction was not remembered")
	}
}

func originKeyForTest(i int) string {
	return originKey(pb.Platform_PLATFORM_DISCORD, callermeta.Origin{
		InstanceUID:    "guild",
		DestinationUID: string(rune('a'+i%26)) + string(rune('a'+i/26)),
	})
}
