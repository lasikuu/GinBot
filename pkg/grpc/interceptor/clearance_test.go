package interceptor

import (
	"context"
	"errors"
	"maps"
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
)

func TestMain(m *testing.M) {
	// log.Z stays nil until a binary calls log.InitializeLogger.
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

const (
	registeredMethod = ginbotv1connect.UserServiceGetUserProcedure
	// moderatorMethod needs CLEARANCE_MODERATOR.
	moderatorMethod = ginbotv1connect.TriggerServiceDeleteTriggerProcedure
	// adminMethod needs CLEARANCE_ADMINISTRATOR.
	adminMethod = ginbotv1connect.InstanceServiceCreateInstanceProcedure
	// publicMethod is absent from testRequirements.
	publicMethod = ginbotv1connect.UtilityServicePingProcedure
)

// testRequirements mirrors the production map without depending on it.
func testRequirements() Requirements {
	return Requirements{
		registeredMethod: pb.Clearance_CLEARANCE_REGISTERED,
		moderatorMethod:  pb.Clearance_CLEARANCE_MODERATOR,
		adminMethod:      pb.Clearance_CLEARANCE_ADMINISTRATOR,
	}
}

// callerAt builds a user_account row at a clearance level; the int32 column
// represents undeclared values too.
func callerAt(clearance int32) *model.User {
	return &model.User{
		ID:        "018f0000-0000-7000-8000-000000000001",
		Username:  "caller",
		Clearance: clearance,
	}
}

// recordingResolver is a CallerResolver with a fixed outcome that records its calls.
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

type callResult struct {
	reached  bool
	caller   *model.User
	callerOK bool
	err      error
}

func call(procedure string, header http.Header, reqs Requirements, resolve CallerResolver) callResult {
	var result callResult

	handler := connect.UnaryFunc(func(handlerCtx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		result.reached = true
		result.caller, result.callerOK = CallerFromContext(handlerCtx)
		return newFakeResponse(), nil
	})

	req := newFakeRequest(procedure)
	maps.Copy(req.Header(), header)

	intercept := NewClearanceInterceptor(reqs, resolve)
	_, result.err = intercept.WrapUnary(handler)(context.Background(), req)

	return result
}

func wellFormedHeader(platform pb.Platform, platformUID string) http.Header {
	header := make(http.Header)
	header.Set(callermeta.HeaderPlatformEnum, platform.String())
	if platformUID != "" {
		header.Set(callermeta.HeaderUserID, platformUID)
	}
	return header
}

func requireCode(t *testing.T, err error, want connect.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error with code %v, got nil", want)
	}

	got := connect.CodeOf(err)
	if got != want {
		t.Fatalf("code = %v, want %v (error: %v)", got, want, err)
	}
}

// Platform clients render these codes to users: an unregistered caller must be
// told to register, not shown a permissions error.
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
		want     connect.Code
	}{
		{
			name:     "no header at all",
			method:   registeredMethod,
			header:   make(http.Header),
			resolver: owner(),
			want:     connect.CodeInvalidArgument,
		},
		{
			// The server reads enum names, not numbers.
			name:     "platform_enum sent as a number",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderPlatformEnum, "1", callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     connect.CodeInvalidArgument,
		},
		{
			name:     "platform_enum is not a known name",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderPlatformEnum, "PLATFORM_CARRIER_PIGEON", callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     connect.CodeInvalidArgument,
		},
		{
			name:     "platform_enum is empty",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderPlatformEnum, "", callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     connect.CodeInvalidArgument,
		},
		{
			name:     "platform_enum is missing",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     connect.CodeInvalidArgument,
		},
		{
			name:     "platform_enum is unspecified",
			method:   registeredMethod,
			header:   rawHeader(callermeta.HeaderPlatformEnum, pb.Platform_PLATFORM_UNSPECIFIED.String(), callermeta.HeaderUserID, "platform-uid"),
			resolver: owner(),
			want:     connect.CodeInvalidArgument,
		},
		{
			name:     "clearance required but no user_id header",
			method:   registeredMethod,
			header:   wellFormedHeader(pb.Platform_PLATFORM_DISCORD, ""),
			resolver: owner(),
			want:     connect.CodeInvalidArgument,
		},
		{
			name:     "caller has no user_account row",
			method:   registeredMethod,
			header:   wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"),
			resolver: &recordingResolver{err: db.ErrNotFound},
			want:     connect.CodeFailedPrecondition,
		},
		{
			name:     "clearance below the minimum",
			method:   adminMethod,
			header:   wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"),
			resolver: &recordingResolver{user: callerAt(int32(pb.Clearance_CLEARANCE_REGISTERED))},
			want:     connect.CodePermissionDenied,
		},
		{
			// A resolver failure must not masquerade as "not registered".
			name:     "resolver fails",
			method:   registeredMethod,
			header:   wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"),
			resolver: &recordingResolver{err: errors.New("connection refused")},
			want:     connect.CodeInternal,
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

// The message must mention registration; the client turns it into "run /register".
func TestUnregisteredCallerIsToldToRegister(t *testing.T) {
	resolver := &recordingResolver{err: db.ErrNotFound}

	got := call(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "unknown"), testRequirements(), resolver.resolve)

	requireCode(t, got.err, connect.CodeFailedPrecondition)
	if message := got.err.Error(); !strings.Contains(strings.ToLower(message), "regist") {
		t.Errorf("message = %q, want it to mention registration", message)
	}
}

// A public method must not resolve the caller, or /ping would fail unregistered users.
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

// Clearance values are non-contiguous, so comparison must be numeric, not by ordering.
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
		{"between registered and member meets registered", pb.Clearance_CLEARANCE_REGISTERED, 5, true},
		{"between registered and member fails member", pb.Clearance_CLEARANCE_MEMBER, 5, false},
		{"above owner meets owner", pb.Clearance_CLEARANCE_OWNER, 1000, true},
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

			requireCode(t, got.err, connect.CodePermissionDenied)
			if got.reached {
				t.Error("handler ran despite insufficient clearance")
			}
		})
	}
}

// A (nil, nil) resolver must be rejected, not panic on user.Clearance.
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

	// The exact code is not pinned, only that it is neither success nor panic.
	switch code := connect.CodeOf(got.err); code {
	case connect.CodeFailedPrecondition, connect.CodeInternal:
	default:
		t.Errorf("code = %v, want FailedPrecondition or Internal", code)
	}
}

// Failing closed must not extend to public methods: probes keep answering.
func TestNilResolverFailsClosedOnAGuardedMethod(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("interceptor panicked with a nil resolver: %v", r)
		}
	}()

	guarded := call(registeredMethod, wellFormedHeader(pb.Platform_PLATFORM_DISCORD, "platform-uid"), testRequirements(), nil)

	// Internal, not FailedPrecondition: the caller did nothing wrong.
	requireCode(t, guarded.err, connect.CodeInternal)
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

	requireCode(t, got.err, connect.CodeInternal)
	if got.reached {
		t.Error("handler ran after the resolver failed")
	}

	// The underlying failure must not leak to the caller.
	if message := got.err.Error(); strings.Contains(message, "connection refused") {
		t.Errorf("message = %q, want the internal failure not to be exposed", message)
	}
}
