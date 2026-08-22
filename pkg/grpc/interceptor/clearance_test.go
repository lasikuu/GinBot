package interceptor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestMain(m *testing.M) {
	// The interceptor logs on its failure paths, and log.Z stays nil until a
	// binary calls log.InitializeLogger — which tests never do.
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// Method names used by the pure tests. They are the generated constants rather
// than string literals so a proto rename breaks compilation here instead of
// silently turning a guarded method into a public one.
const (
	// registeredMethod needs at least CLEARANCE_REGISTERED in testRequirements.
	registeredMethod = pb.UserService_GetUser_FullMethodName
	// moderatorMethod needs exactly CLEARANCE_MODERATOR.
	moderatorMethod = pb.TriggerService_DeleteTrigger_FullMethodName
	// adminMethod needs CLEARANCE_ADMINISTRATOR.
	adminMethod = pb.InstanceService_CreateInstance_FullMethodName
	// publicMethod is absent from testRequirements.
	publicMethod = pb.UtilityService_Ping_FullMethodName
)

// testRequirements mirrors the shape of the production map without depending on
// it: some public methods (absent) and three tiers of guarded method.
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
// and that the metadata reaches the resolver intact.
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

// call runs a request for method through the clearance interceptor. The request
// payload is deliberately not a protobuf message: clearance must not care.
func call(ctx context.Context, method string, reqs Requirements, resolve CallerResolver) callResult {
	var result callResult

	handler := func(handlerCtx context.Context, _ any) (any, error) {
		result.reached = true
		result.caller, result.callerOK = CallerFromContext(handlerCtx)
		return "handled", nil
	}

	intercept := NewClearanceUnaryInterceptor(reqs, resolve)
	_, result.err = intercept(ctx, struct{}{}, &grpc.UnaryServerInfo{FullMethod: method}, handler)

	return result
}

// incomingCtx builds the incoming context that a client's outgoing metadata
// produces. It round-trips through callermeta so the test cannot disagree with
// the production encoding the way the Discord client once did.
func incomingCtx(t *testing.T, platform pb.Platform, platformUID string) context.Context {
	t.Helper()

	md, ok := metadata.FromOutgoingContext(callermeta.NewOutgoingContext(context.Background(), platform, platformUID))
	if !ok {
		t.Fatal("callermeta attached no outgoing metadata")
	}

	return metadata.NewIncomingContext(context.Background(), md)
}

// requireCode asserts the exact gRPC code carried by err.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error with code %v, got nil", want)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	if st.Code() != want {
		t.Fatalf("code = %v, want %v (message: %q)", st.Code(), want, st.Message())
	}
}

// The error mapping is the contract platform clients render to users: an
// unregistered caller must be told to register, not shown a permissions error.
func TestClearanceErrorMapping(t *testing.T) {
	// rawMetadata builds an incoming context bypassing callermeta, which is the
	// only way to produce metadata a real client could never send.
	rawMetadata := func(pairs ...string) func(*testing.T) context.Context {
		return func(*testing.T) context.Context {
			return metadata.NewIncomingContext(context.Background(), metadata.Pairs(pairs...))
		}
	}
	wellFormed := func(t *testing.T) context.Context {
		return incomingCtx(t, pb.Platform_PLATFORM_DISCORD, "platform-uid")
	}

	owner := func() *recordingResolver {
		return &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}
	}

	tests := []struct {
		name     string
		method   string
		ctx      func(*testing.T) context.Context
		resolver *recordingResolver
		want     codes.Code
	}{
		{
			name:     "no metadata at all",
			method:   registeredMethod,
			ctx:      func(*testing.T) context.Context { return context.Background() },
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			// The pre-callermeta client sent the enum number; the server reads names.
			name:     "platform_enum sent as a number",
			method:   registeredMethod,
			ctx:      rawMetadata(callermeta.KeyPlatformEnum, "1", callermeta.KeyUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			name:     "platform_enum is not a known name",
			method:   registeredMethod,
			ctx:      rawMetadata(callermeta.KeyPlatformEnum, "PLATFORM_CARRIER_PIGEON", callermeta.KeyUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			name:     "platform_enum is empty",
			method:   registeredMethod,
			ctx:      rawMetadata(callermeta.KeyPlatformEnum, "", callermeta.KeyUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			name:     "platform_enum is missing",
			method:   registeredMethod,
			ctx:      rawMetadata(callermeta.KeyUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			name:     "platform_enum is unspecified",
			method:   registeredMethod,
			ctx:      rawMetadata(callermeta.KeyPlatformEnum, pb.Platform_PLATFORM_UNSPECIFIED.String(), callermeta.KeyUserID, "platform-uid"),
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			// A guarded method cannot run without knowing who is calling.
			name:     "clearance required but no user_id metadata",
			method:   registeredMethod,
			ctx:      func(t *testing.T) context.Context { return incomingCtx(t, pb.Platform_PLATFORM_DISCORD, "") },
			resolver: owner(),
			want:     codes.InvalidArgument,
		},
		{
			// db.GetUserByPlatformUID is the production resolver and reports a
			// missing row with ErrNotFound.
			name:     "caller has no user_account row",
			method:   registeredMethod,
			ctx:      wellFormed,
			resolver: &recordingResolver{err: db.ErrNotFound},
			want:     codes.FailedPrecondition,
		},
		{
			name:     "clearance below the minimum",
			method:   adminMethod,
			ctx:      wellFormed,
			resolver: &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))},
			want:     codes.PermissionDenied,
		},
		{
			// A resolver failure is the server's problem, not the caller's; it
			// must not masquerade as "you are not registered".
			name:     "resolver fails",
			method:   registeredMethod,
			ctx:      wellFormed,
			resolver: &recordingResolver{err: errors.New("connection refused")},
			want:     codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := call(tt.ctx(t), tt.method, testRequirements(), tt.resolver.resolve)

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

	got := call(incomingCtx(t, pb.Platform_PLATFORM_DISCORD, "unknown"), registeredMethod, testRequirements(), resolver.resolve)

	requireCode(t, got.err, codes.FailedPrecondition)
	if message := status.Convert(got.err).Message(); !strings.Contains(strings.ToLower(message), "regist") {
		t.Errorf("message = %q, want it to mention registration", message)
	}
}

// A public method that resolves the caller anyway defeats the point of the map:
// /ping would start failing for anyone who has not registered.
func TestPublicMethodNeedsNoMetadataAndResolvesNobody(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := call(context.Background(), publicMethod, testRequirements(), resolver.resolve)

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

func TestPublicMethodWithMetadataStillSucceeds(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := call(incomingCtx(t, pb.Platform_PLATFORM_DISCORD, "platform-uid"), publicMethod, testRequirements(), resolver.resolve)

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
// not blow up: gRPC rejects unknown methods itself.
func TestUnknownMethodIsTreatedAsPublic(t *testing.T) {
	resolver := &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_OWNER))}

	got := call(context.Background(), "/ginbot.proto.NoSuchService/NoSuchMethod", testRequirements(), resolver.resolve)

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

	got := call(context.Background(), adminMethod, nil, resolver.resolve)

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

	got := call(incomingCtx(t, pb.Platform_PLATFORM_MATRIX_PROTOCOL, "@a:example.org"), adminMethod, testRequirements(), resolver.resolve)

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

			got := call(incomingCtx(t, pb.Platform_PLATFORM_DISCORD, "platform-uid"), registeredMethod, reqs, resolver.resolve)

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

	got := call(incomingCtx(t, pb.Platform_PLATFORM_DISCORD, "platform-uid"), registeredMethod, testRequirements(), resolver.resolve)

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
	switch code := status.Code(got.err); code {
	case codes.FailedPrecondition, codes.Internal:
	default:
		t.Errorf("code = %v, want FailedPrecondition or Internal", code)
	}
}

func TestResolverErrorIsRejectedWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("interceptor panicked on a failing resolver: %v", r)
		}
	}()

	resolver := &recordingResolver{err: errors.New("dial tcp: connection refused")}

	got := call(incomingCtx(t, pb.Platform_PLATFORM_DISCORD, "platform-uid"), registeredMethod, testRequirements(), resolver.resolve)

	requireCode(t, got.err, codes.Internal)
	if got.reached {
		t.Error("handler ran after the resolver failed")
	}

	// The underlying failure must not leak to the caller.
	if message := status.Convert(got.err).Message(); strings.Contains(message, "connection refused") {
		t.Errorf("message = %q, want the internal failure not to be exposed", message)
	}
}
