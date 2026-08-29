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

// These run against a real Connect client/server pair: req.Spec().Procedure is
// populated by CallUnary from the schema, not by connect.NewRequest, so a
// hand-built request would test against Procedure == "".

// scriptedPingServer answers Ping with a failing code for the first `failures`
// attempts, then succeeds. code defaults to connect.CodeUnavailable.
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

// scriptedCreateReminderServer always fails CreateReminder with CodeUnavailable,
// the code that would be retried were CreateReminder allowlisted; it is not,
// because retrying a mutation could double-create the reminder.
type scriptedCreateReminderServer struct {
	ginbotv1connect.UnimplementedReminderServiceHandler

	attempts atomic.Int32
}

func (s *scriptedCreateReminderServer) CreateReminder(context.Context, *connect.Request[pb.CreateReminderReq]) (*connect.Response[pb.CreateReminderResp], error) {
	s.attempts.Add(1)
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("scripted failure"))
}

// retryHarness is a running server plus clients for both services, with
// newRetryInterceptor installed as Dial would.
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

// Two transient CodeUnavailable failures then success must be invisible to the
// caller, at the cost of exactly two retries.
func TestNewRetryInterceptorRetriesAnAllowlistedProcedureOnUnavailable(t *testing.T) {
	ping := &scriptedPingServer{failures: 2}
	h := newRetryHarness(t, ping, &scriptedCreateReminderServer{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := h.Utility.Ping(ctx, connect.NewRequest(pb.PingReq_builder{}.Build())); err != nil {
		t.Fatalf("Ping failed after retries: %v", err)
	}

	if got := ping.attemptCount(); got != 3 {
		t.Errorf("Ping was attempted %d times, want 3 (1 initial + 2 retries)", got)
	}
}

// A non-allowlisted mutation must be attempted exactly once, whatever the code.
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

// Only CodeUnavailable is retried; any other code is returned on the first try.
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

// An allowlisted procedure that never recovers must give up after 2 retries.
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

// A deadline that cannot afford the next backoff must make the interceptor give
// up rather than sleep out a backoff it could not honour.
func TestNewRetryInterceptorRespectsADeadlineExpiringMidBackoff(t *testing.T) {
	ping := &scriptedPingServer{failures: 1000}
	h := newRetryHarness(t, ping, &scriptedCreateReminderServer{})

	// Survives one round trip and the first 100ms backoff, but not the second.
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

	// Under the full ~300ms backoff schedule, so ignoring the deadline fails here.
	if elapsed >= 400*time.Millisecond {
		t.Errorf("Ping took %v to fail, want well under the full backoff schedule; "+
			"the interceptor does not appear to be respecting the %v deadline", elapsed, budget)
	}
}

// GetFile is server-streaming now, so it must not be in the unary allowlist.
func TestGetFileIsNotRetryable(t *testing.T) {
	if retryableProcedures[ginbotv1connect.TriggerServiceGetFileProcedure] {
		t.Error("TriggerServiceGetFileProcedure is still in retryableProcedures; " +
			"GetFile is server-streaming now and was removed from the unary retry allowlist")
	}
}

type fakeStreamingClientConnForRetry struct {
	connect.StreamingClientConn
}

// Retries are unary-only: retrying a broken bidi stream would open a second one
// server-side. Asserted as an identity pass-through.
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
