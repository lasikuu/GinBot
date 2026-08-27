package interceptor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
)

func TestMain(m *testing.M) {
	// The interceptor logs on its failure paths, and log.Z stays nil until a
	// binary calls log.InitializeLogger — which tests never do.
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// Procedures used by the pure tests. They are the generated
// ginbotv1connect.*Procedure constants rather than string literals so a proto
// rename breaks compilation here instead of silently turning a guarded method
// into a public one.
const (
	// registeredMethod needs at least CLEARANCE_REGISTERED in testRequirements.
	registeredMethod = ginbotv1connect.UserServiceGetUserProcedure
	// moderatorMethod needs exactly CLEARANCE_MODERATOR.
	moderatorMethod = ginbotv1connect.TriggerServiceDeleteTriggerProcedure
	// adminMethod needs CLEARANCE_ADMINISTRATOR.
	adminMethod = ginbotv1connect.InstanceServiceCreateInstanceProcedure
	// publicMethod is absent from testRequirements.
	publicMethod = ginbotv1connect.UtilityServicePingProcedure
)

// testRequirements mirrors the shape of the production map without depending
// on it: some public methods (absent) and three tiers of guarded method.
func testRequirements() Requirements {
	return Requirements{
		registeredMethod: pb.Clearance_CLEARANCE_REGISTERED,
		moderatorMethod:  pb.Clearance_CLEARANCE_MODERATOR,
		adminMethod:      pb.Clearance_CLEARANCE_ADMINISTRATOR,
	}
}

// callerAt builds a user_account row at a clearance level. Clearance is an
// int32 on the row, so out-of-band values (a level that is not a declared enum
// member) are representable and are worth testing.
func callerAt(clearance int32) *model.User {
	return &model.User{
		ID:        "018f0000-0000-7000-8000-000000000001",
		Username:  "caller",
		Clearance: clearance,
	}
}

// recordingResolver is a CallerResolver with a fixed outcome that records how
// it was called, so tests can assert both that public methods resolve nobody
// and that the header identity reaches the resolver intact.
type recordingResolver struct {
	user *model.User
	err  error

	mu           sync.Mutex
	calls        int
	lastPlatform pb.Platform
	lastUID      string
}

func (r *recordingResolver) resolve(_ context.Context, platform pb.Platform, platformUID string) (*model.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls++
	r.lastPlatform = platform
	r.lastUID = platformUID

	return r.user, r.err
}

func (r *recordingResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *recordingResolver) lastCall() (pb.Platform, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPlatform, r.lastUID
}

// callResult is everything one trip through the clearance interceptor produced.
type callResult struct {
	reached  bool
	caller   *model.User
	callerOK bool
	err      error
}

// call runs a request for procedure, carrying header, through the clearance
// interceptor.
func call(procedure string, header http.Header, reqs Requirements, resolve CallerResolver) callResult {
	var result callResult

	handler := connect.UnaryFunc(func(handlerCtx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		result.reached = true
		result.caller, result.callerOK = CallerFromContext(handlerCtx)
		return newFakeResponse(), nil
	})

	req := newFakeRequest(procedure)
	for key, values := range header {
		req.Header()[key] = values
	}

	intercept := NewClearanceInterceptor(reqs, resolve)
	_, result.err = intercept.WrapUnary(handler)(context.Background(), req)

	return result
}

// wellFormedHeader builds the header a real platform client sends, via
// callermeta itself so the test cannot disagree with the production encoding.
func wellFormedHeader(platform pb.Platform, platformUID string) http.Header {
	header := make(http.Header)
	header.Set(callermeta.HeaderPlatformEnum, platform.String())
	if platformUID != "" {
		header.Set(callermeta.HeaderUserID, platformUID)
	}
	return header
}

// requireCode asserts the exact Connect code carried by err.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error with code %v, got nil", want)
	}

	got := connect.CodeOf(err)
	wantConnect := connect.Code(uint32(want))
	if got != wantConnect {
		t.Fatalf("code = %v, want %v (error: %v)", got, wantConnect, err)
	}
}

// The error mapping is the contract platform clients render to users: an
// unregistered caller must be told to register, not shown a permissions error.
func TestClearanceErrorMapping(t *testing.T) {
	rawHeader := func(pairs ...string) http.Header {
		header := make(http.Header)
		for i := 0; i+1 < len(pairs); i += 2 {
			header.Set(pairs[i], pairs[i+1])
		}
		return header
	}

	owner := func() *recordingResolver {
		return &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}
	}

	tests := []struct {
		name     string
		method   string
		header   http.Header
		resolver *recordingResolver
		want     codes.Code
	}{
		{
			name:     "no header at all",
			method:   registeredMethod,
			header:   make(http.Header),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			// The pre-callermeta client sent the enum number; the server reads names.
			name:     "platform_enum sent as a number",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderPlatformEnum, "1", callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			name:     "platform_enum is not a known name",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderPlatformEnum, "PLATFORM_CARRIER_PIGEON", callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			name:     "platform_enum is empty",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderPlatformEnum, "", callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			name:     "platform_enum is missing",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			name:     "platform_enum is unspecified",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderPlatformEnum, pb.Platform_PLATFORM_UNSPECIFIED.String(), callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			// A guarded method cannot run without knowing who is calling.
			name:     "clearance required but no user_id header",
			method:   registeredMethod,
			header:   wellFormedHeader(pb.Platform_PLATFORM_DISCORD, ""),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			// db.GetUserByPlatformUID is the production resolver and reports a
			// missing row with ErrNotFound.
			name:     "caller has no user_account row",
			method:   registeredMethod,
			header:   wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"),
			resolver: &recordingResolver{err: db.ErrNotFound},
			want:     codes.FailedPrecondition,
		},
		{
			name:     "clearance below the minimum",
			method:   adminMethod,
			header:   wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"),
			resolver: &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))},
			want:     codes.PermissionDenied,
		},
		{
			// A resolver failure is the server's problem, not the caller's; it
			// must not masquerade as "you are not registered".
			name:     "resolver fails",
			method:   registeredMethod,
			header:   wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"),
			resolver: &recordingResolver{err: errors.New("connection refused")},
			want:     codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := call(tt.method, tt.header, testRequirements(), tt.resolver.resolve)

			requireCode(t, got.err, tt.want)
			if got.reached {
				t.Error("handler ran despite the call being rejected")
			}
		})
	}
}

// The client turns this into "run /register first", so the message has to say so.
func TestUnregisteredCallerIsToldToRegister(t *testing.T) {
	resolver := &recordingResolver{err: db.ErrNotFound}

	got := call(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "unknown"), testRequirements(), resolver.resolve)

	requireCode(t, got.err, codes.FailedPrecondition)
	if message := got.err.Error(); !strings.Contains(strings.ToLower(message), "regist") {
		t.Errorf("message = %q, want it to mention registration", message)
	}
}

// A public method that resolves the caller anyway defeats the point of the map:
// /ping would start failing for anyone who has not registered.
func TestPublicMethodNeedsNoHeaderAndResolvesNobody(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := call(publicMethod, make(http.Header), testRequirements(), resolver.resolve)

	if got.err != nil {
		t.Fatalf("public method rejected: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times for a public method, want 0", n)
	}
	if got.callerOK {
		t.Error("CallerFromContext returned a caller on a public method")
	}
}

func TestPublicMethodWithHeaderStillSucceeds(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := call(publicMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"), testRequirements(), resolver.resolve)

	if got.err != nil {
		t.Fatalf("public method rejected: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times for a public method, want 0", n)
	}
}

// A method nobody declared must not be guarded by accident, and equally must
// not blow up: Connect rejects unknown procedures itself.
func TestUnknownMethodIsTreatedAsPublic(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := call("/ginbot.v1.NoSuchService/NoSuchMethod", make(http.Header), testRequirements(), resolver.resolve)

	if got.err != nil {
		t.Fatalf("unknown method rejected: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times, want 0", n)
	}
}

// An empty map declares nothing, so nothing is guarded. Worth pinning: the
// alternative reading — "unknown means deny" — would lock every client out if
// the map were ever dropped from the server options.
func TestNilRequirementsMakesEverythingPublic(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := call(adminMethod, make(http.Header), nil, resolver.resolve)

	if got.err != nil {
		t.Fatalf("call rejected with no requirements declared: %v", got.err)
	}
	if !got.reached {
		t.Error("handler was not reached")
	}
	if n := resolver.callCount(); n != 0 {
		t.Errorf("resolver ran %d times, want 0", n)
	}
}

func TestSufficientClearanceReachesTheHandler(t *testing.T) {
	caller := callerAt(int32(pb.Clearance_CLEARANCE_ADMINISTRATOR))
	resolver := &recordingResolver{user: caller}

	got := call(adminMethod, wellFormedHeader(pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org"), testRequirements(), resolver.resolve)

	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if !got.reached {
		t.Fatal("handler was not reached")
	}
	if !got.callerOK {
		t.Fatal("CallerFromContext found no caller inside the handler")
	}
	if got.caller != caller {
		t.Errorf("handler saw caller %+v, want the resolved one %+v", got.caller, caller)
	}

	if n := resolver.callCount(); n != 1 {
		t.Errorf("resolver ran %d times, want exactly 1", n)
	}
	platform, uid := resolver.lastCall()
	if platform != pb.Platform_PLATFORM_MATRIX_PROTOCOL {
		t.Errorf("resolver got platform %v, want %v", platform, pb.Platform_PLATFORM_MATRIX_PROTOCOL)
	}
	if uid != "@a:example.org" {
		t.Errorf("resolver got uid %q, want %q", uid, "@a:example.org")
	}
}

func TestCallerFromContextOnABareContext(t *testing.T) {
	caller, ok := CallerFromContext(context.Background())
	if ok {
		t.Errorf("ok = true on a bare context, caller = %+v", caller)
	}
	if caller != nil {
		t.Errorf("caller = %+v, want nil", caller)
	}
}

// Clearance values are non-contiguous (0, 1, 10, 20, 50, 100), so a comparison
// written against enum ordering rather than the numeric value would let MEMBER
// through a MODERATOR gate.
func TestClearanceBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		required pb.Clearance
		caller   int32
		allow    bool
	}{
		{"unspecified caller fails registered", pb.Clearance_CLEARANCE_REGISTERED, int32(pb.Clearance_CLEARANCE_UNSPECIFIED), false},
		{"unspecified caller fails moderator", pb.Clearance_CLEARANCE_MODERATOR, int32(pb.Clearance_CLEARANCE_UNSPECIFIED), false},
		{"unspecified caller fails administrator", pb.Clearance_CLEARANCE_ADMINISTRATOR, int32(pb.Clearance_CLEARANCE_UNSPECIFIED), false},
		{"registered meets registered exactly", pb.Clearance_CLEARANCE_REGISTERED, int32(pb.Clearance_CLEARANCE_REGISTERED), true},
		{"registered fails member", pb.Clearance_CLEARANCE_MEMBER, int32(pb.Clearance_CLEARANCE_REGISTERED), false},
		{"registered fails administrator", pb.Clearance_CLEARANCE_ADMINISTRATOR, int32(pb.Clearance_CLEARANCE_REGISTERED), false},
		{"member exceeds registered", pb.Clearance_CLEARANCE_REGISTERED, int32(pb.Clearance_CLEARANCE_MEMBER), true},
		{"member meets member exactly", pb.Clearance_CLEARANCE_MEMBER, int32(pb.Clearance_CLEARANCE_MEMBER), true},
		{"member fails moderator", pb.Clearance_CLEARANCE_MODERATOR, int32(pb.Clearance_CLEARANCE_MEMBER), false},
		{"moderator meets moderator exactly", pb.Clearance_CLEARANCE_MODERATOR, int32(pb.Clearance_CLEARANCE_MODERATOR), true},
		{"moderator fails administrator", pb.Clearance_CLEARANCE_ADMINISTRATOR, int32(pb.Clearance_CLEARANCE_MODERATOR), false},
		{"administrator exceeds moderator", pb.Clearance_CLEARANCE_MODERATOR, int32(pb.Clearance_CLEARANCE_ADMINISTRATOR), true},
		{"administrator meets administrator exactly", pb.Clearance_CLEARANCE_ADMINISTRATOR, int32(pb.Clearance_CLEARANCE_ADMINISTRATOR), true},
		{"administrator fails owner", pb.Clearance_CLEARANCE_OWNER, int32(pb.Clearance_CLEARANCE_ADMINISTRATOR), false},
		{"owner meets registered", pb.Clearance_CLEARANCE_REGISTERED, int32(pb.Clearance_CLEARANCE_OWNER), true},
		{"owner meets moderator", pb.Clearance_CLEARANCE_MODERATOR, int32(pb.Clearance_CLEARANCE_OWNER), true},
		{"owner meets administrator", pb.Clearance_CLEARANCE_ADMINISTRATOR, int32(pb.Clearance_CLEARANCE_OWNER), true},
		{"owner meets owner exactly", pb.Clearance_CLEARANCE_OWNER, int32(pb.Clearance_CLEARANCE_OWNER), true},
		// The column is a plain int, so rows can hold values that are not
		// declared enum members. They must compare numerically, not be treated
		// as unknown-and-therefore-denied or unknown-and-therefore-allowed.
		{"between registered and member meets registered", pb.Clearance_CLEARANCE_REGISTERED, 5, true},
		{"between registered and member fails member", pb.Clearance_CLEARANCE_MEMBER, 5, false},
		{"above owner meets owner", pb.Clearance_CLEARANCE_OWNER, 1000, true},
		// A corrupt or negative row must never satisfy anything.
		{"negative clearance fails registered", pb.Clearance_CLEARANCE_REGISTERED, -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs := Requirements{registeredMethod: tt.required}
			resolver := &recordingResolver{user: callerAt(tt.caller)}

			got := call(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"), reqs, resolver.resolve)

			if tt.allow {
				if got.err != nil {
					t.Fatalf("clearance %d was denied a %v gate: %v", tt.caller, tt.required, got.err)
				}
				if !got.reached {
					t.Error("handler was not reached")
				}
				return
			}

			requireCode(t, got.err, codes.PermissionDenied)
			if got.reached {
				t.Error("handler ran despite insufficient clearance")
			}
		})
	}
}

// A nil user with a nil error is not the same as an error, and the obvious
// implementation — read user.Clearance straight after resolving — panics on it,
// taking the whole server process down.
func TestResolverReturningNoUserAndNoErrorIsRejectedWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("interceptor panicked on a (nil, nil) resolver: %v", r)
		}
	}()

	resolver := &recordingResolver{user: nil, err: nil}

	got := call(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"), testRequirements(), resolver.resolve)

	if got.err == nil {
		t.Fatal("a resolver that produced no caller was allowed through")
	}
	if got.reached {
		t.Error("handler ran without a resolved caller")
	}

	// The exact code is deliberately not pinned: "no user and no error" is a
	// resolver contract violation that the specification does not map, and both
	// FailedPrecondition (treat as unregistered) and Internal (treat as a bug)
	// are defensible. What matters is that it is neither a success nor a panic.
	switch code := connect.CodeOf(got.err); code {
	case connect.CodeFailedPrecondition, connect.CodeInternal:
	default:
		t.Errorf("code = %v, want FailedPrecondition or Internal", code)
	}
}

// TestNilResolverFailsClosedOnAGuardedMethod.
//
// A nil CallerResolver on a guarded method is a wiring bug — the server was
// constructed without one — and the only two possible behaviours are "deny
// everything" and "dereference nil on the first guarded request". This is the
// one interceptor failure path the rest of this file did not reach: the
// resolver-returns-an-error and resolver-returns-(nil, nil) cases both supply
// a resolver, so neither exercises the guard at all.
//
// The public path is asserted alongside it, because failing closed must not
// mean failing closed for /ping too: health checks and latency probes have to
// keep answering while identity is broken, which is precisely when someone is
// looking at them.
func TestNilResolverFailsClosedOnAGuardedMethod(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("interceptor panicked with a nil resolver: %v", r)
		}
	}()

	guarded := call(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"), testRequirements(), nil)

	// Internal, not PermissionDenied or FailedPrecondition: the caller did
	// nothing wrong and must not be told to register or to ask for access.
	requireCode(t, guarded.err, codes.Internal)
	if guarded.reached {
		t.Error("handler ran on a guarded method with no caller resolver configured")
	}

	public := call(publicMethod, make(http.Header), testRequirements(), nil)
	if public.err != nil {
		t.Errorf("public method rejected with a nil resolver: %v", public.err)
	}
	if !public.reached {
		t.Error("public handler was not reached with a nil resolver")
	}
}

func TestResolverErrorIsRejectedWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("interceptor panicked on a failing resolver: %v", r)
		}
	}()

	resolver := &recordingResolver{err: errors.New("dial tcp: connection refused")}

	got := call(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"), testRequirements(), resolver.resolve)

	requireCode(t, got.err, codes.Internal)
	if got.reached {
		t.Error("handler ran after the resolver failed")
	}

	// The underlying failure must not leak to the caller.
	if message := got.err.Error(); strings.Contains(message, "connection refused") {
		t.Errorf("message = %q, want the internal failure not to be exposed", message)
	}
}
