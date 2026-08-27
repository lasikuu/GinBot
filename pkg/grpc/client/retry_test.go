package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
)

// newRetryInterceptor decides what to retry by reading req.Spec().Procedure,
// which connect.NewRequest alone never populates — that field is filled in by
// connect.Client.CallUnary from the schema the generated client was built
// with. So unlike newDeadlineInterceptor's tests, these have to run against a
// real Connect client and server pair rather than a hand-built
// connect.AnyRequest, or every case would silently test against Procedure ==
// "" and prove nothing about the allowlist.
//
// The harness here mounts exactly two services — UtilityService (Ping is
// allowlisted) and ReminderService (CreateReminder is not) — which is the
// minimum needed to exercise both the "retry" and "do not retry" halves of
// the allowlist over one real transport.

// scriptedPingServer answers UtilityService.Ping with connect.CodeUnavailable
// for the first `failures` attempts, then succeeds. code overrides the error
// code returned on those failing attempts; the zero value is
// connect.CodeUnavailable.
type scriptedPingServer struct {
	ginbotv1connect.UnimplementedUtilityServiceHandler

	failures int
	code     connect.Code

	mu       sync.Mutex
	attempts int
}

func (s *scriptedPingServer) Ping(_ context.Context, _ *connect.Request[pb.PingReq]) (*connect.Response[pb.PingResp], error) {
	s.mu.Lock()
	s.attempts++
	attempt := s.attempts
	s.mu.Unlock()

	if attempt <= s.failures {
		code := s.code
		if code == 0 {
			code = connect.CodeUnavailable
		}
		return nil, connect.NewError(code, errors.New("scripted failure"))
	}

	return connect.NewResponse(pb.PingResp_builder{}.Build()), nil
}

func (s *scriptedPingServer) attemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

// scriptedCreateReminderServer always fails CreateReminder with
// CodeUnavailable, the code that WOULD be retried on an allowlisted
// procedure. CreateReminder is not allowlisted precisely because it mutates:
// a client that retried it on a response the server never actually sent could
// double-create the reminder.
type scriptedCreateReminderServer struct {
	ginbotv1connect.UnimplementedReminderServiceHandler

	attempts atomic.Int32
}

func (s *scriptedCreateReminderServer) CreateReminder(context.Context, *connect.Request[pb.CreateReminderReq]) (*connect.Response[pb.CreateReminderResp], error) {
	s.attempts.Add(1)
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("scripted failure"))
}

// retryHarness is a running server plus clients for both services, built with
// newRetryInterceptor installed exactly the way Dial would install it.
type retryHarness struct {
	Utility  ginbotv1connect.UtilityServiceClient
	Reminder ginbotv1connect.ReminderServiceClient
}

func newRetryHarness(t *testing.T, ping *scriptedPingServer, createReminder *scriptedCreateReminderServer) *retryHarness {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewUtilityServiceHandler(ping))
	mux.Handle(ginbotv1connect.NewReminderServiceHandler(createReminder))

	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	httpClient := srv.Client()
	t.Cleanup(httpClient.CloseIdleConnections)

	clientOpts := connect.WithInterceptors(newRetryInterceptor())

	return &retryHarness{
		Utility:  ginbotv1connect.NewUtilityServiceClient(httpClient, srv.URL, clientOpts),
		Reminder: ginbotv1connect.NewReminderServiceClient(httpClient, srv.URL, clientOpts),
	}
}

// TestNewRetryInterceptorRetriesAnAllowlistedProcedureOnUnavailable is the
// happy retry path: two transient CodeUnavailable failures followed by a
// success must be invisible to the caller, at the cost of exactly two extra
// attempts — not the deadline interceptor's default budget's worth of
// hammering, and not zero.
func TestNewRetryInterceptorRetriesAnAllowlistedProcedureOnUnavailable(t *testing.T) {
	ping := &scriptedPingServer{failures: 2}
	h := newRetryHarness(t, ping, &scriptedCreateReminderServer{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := h.Utility.Ping(ctx, connect.NewRequest(pb.PingReq_builder{}.Build())); err != nil {
		t.Fatalf("Ping failed after retries: %v", err)
	}

	// 1 initial attempt + 2 retries.
	if got := ping.attemptCount(); got != 3 {
		t.Errorf("Ping was attempted %d times, want 3 (1 initial + 2 retries)", got)
	}
}

// TestNewRetryInterceptorDoesNotRetryAMutatingProcedure is the refusal path
// that gives the allowlist its whole reason to exist: CreateReminder must be
// attempted exactly once, whatever the code, because a client-side retry of a
// write the server may already have applied is a duplicate reminder, not a
// safety net.
func TestNewRetryInterceptorDoesNotRetryAMutatingProcedure(t *testing.T) {
	createReminder := &scriptedCreateReminderServer{}
	h := newRetryHarness(t, &scriptedPingServer{}, createReminder)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := h.Reminder.CreateReminder(ctx, connect.NewRequest(pb.CreateReminderReq_builder{}.Build()))
	if err == nil {
		t.Fatal("CreateReminder succeeded; the scripted server always fails it")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Errorf("code = %v, want %v", got, connect.CodeUnavailable)
	}

	if got := createReminder.attempts.Load(); got != 1 {
		t.Errorf("CreateReminder was attempted %d times, want exactly 1 (not allowlisted, so never retried)", got)
	}
}

// TestNewRetryInterceptorDoesNotRetryANonUnavailableCode: only
// CodeUnavailable is transient in the way this interceptor cares about. A
// caller-facing rejection dressed up as anything else (InvalidArgument,
// PermissionDenied, ...) retried automatically would just mean the same
// refusal three times slower.
func TestNewRetryInterceptorDoesNotRetryANonUnavailableCode(t *testing.T) {
	ping := &scriptedPingServer{failures: 100, code: connect.CodeInternal}
	h := newRetryHarness(t, ping, &scriptedCreateReminderServer{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := h.Utility.Ping(ctx, connect.NewRequest(pb.PingReq_builder{}.Build()))
	if err == nil {
		t.Fatal("Ping succeeded; the scripted server always fails it")
	}
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want %v", got, connect.CodeInternal)
	}
	if got := ping.attemptCount(); got != 1 {
		t.Errorf("Ping was attempted %d times, want exactly 1 (CodeInternal is not retried)", got)
	}
}

// TestNewRetryInterceptorStopsAtTheCeiling: an allowlisted procedure that
// NEVER recovers must still give up, at most 2 retries in, rather than retry
// forever inside one logical call.
func TestNewRetryInterceptorStopsAtTheCeiling(t *testing.T) {
	ping := &scriptedPingServer{failures: 1000}
	h := newRetryHarness(t, ping, &scriptedCreateReminderServer{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := h.Utility.Ping(ctx, connect.NewRequest(pb.PingReq_builder{}.Build()))
	if err == nil {
		t.Fatal("Ping succeeded; the scripted server never recovers")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Errorf("code = %v, want %v", got, connect.CodeUnavailable)
	}
	if got := ping.attemptCount(); got != 3 {
		t.Errorf("Ping was attempted %d times, want exactly 3 (1 initial + at most 2 retries)", got)
	}
}

// TestNewRetryInterceptorRespectsADeadlineExpiringMidBackoff: the schedule is
// 100ms then 200ms. A deadline generous enough to survive the first backoff
// but not the second must make the interceptor give up before reaching the
// 3-attempt ceiling TestNewRetryInterceptorStopsAtTheCeiling pins under an
// ample deadline — it must not sleep out a backoff it could not have honoured
// and only THEN notice the deadline had already passed.
//
// The bound is deliberately generous rather than tight: this is timing
// against a real in-process TLS handshake, which costs a few to a few dozen
// milliseconds depending on the machine. What is asserted is the invariant
// that actually matters — the retry ceiling was never reached — not a
// specific attempt count, which a slow first handshake could shift by one
// without the interceptor doing anything wrong.
func TestNewRetryInterceptorRespectsADeadlineExpiringMidBackoff(t *testing.T) {
	ping := &scriptedPingServer{failures: 1000}
	h := newRetryHarness(t, ping, &scriptedCreateReminderServer{})

	// Long enough to survive one round trip and the first 100ms backoff,
	// short enough that the remaining budget after the second failure is
	// under the 200ms second backoff.
	const budget = 250 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	_, err := h.Utility.Ping(ctx, connect.NewRequest(pb.PingReq_builder{}.Build()))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Ping succeeded; the scripted server never recovers")
	}

	if got := ping.attemptCount(); got >= 3 {
		t.Errorf("Ping was attempted %d times, want fewer than the 3-attempt ceiling; "+
			"a %v deadline cannot afford the full 100ms+200ms backoff schedule", got, budget)
	}

	// Comfortably under what the full ceiling schedule takes
	// (TestNewRetryInterceptorStopsAtTheCeiling, ~300ms of backoff alone), so
	// a regression that ignored the deadline and ran the whole schedule
	// anyway still fails this even with generous jitter.
	if elapsed >= 400*time.Millisecond {
		t.Errorf("Ping took %v to fail, want well under the full backoff schedule; "+
			"the interceptor does not appear to be respecting the %v deadline", elapsed, budget)
	}
}

// fakeStreamingClientConnForRetry mirrors fakeStreamingClientConn in
// deadline_test.go: WrapStreamingClient's job here is only to prove it passes
// a streaming call straight through unmodified, so nothing beyond the seam
// itself needs a real implementation.
type fakeStreamingClientConnForRetry struct {
	connect.StreamingClientConn
}

// TestNewRetryInterceptorWrapStreamingClientIsAPassThrough: the specification
// is explicit that retries are unary-only. OpenClientActionStream is a
// long-lived bidi stream — silently retrying a broken one would open a SECOND
// stream server-side rather than resume the first, doubling the client's
// registration. Asserted as an identity pass-through: the same conn instance
// next returns must come back unchanged.
func TestNewRetryInterceptorWrapStreamingClientIsAPassThrough(t *testing.T) {
	interceptor := newRetryInterceptor()

	want := &fakeStreamingClientConnForRetry{}
	calls := 0
	next := connect.StreamingClientFunc(func(context.Context, connect.Spec) connect.StreamingClientConn {
		calls++
		return want
	})

	got := interceptor.WrapStreamingClient(next)(context.Background(), connect.Spec{})

	if calls != 1 {
		t.Errorf("next was called %d times, want exactly 1 (no retry loop around a stream open)", calls)
	}
	if got != want {
		t.Error("WrapStreamingClient did not pass the streaming conn through unchanged")
	}
}
