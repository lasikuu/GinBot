package client

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// deadlineOf runs req through interceptor's WrapUnary and reports the deadline
// next observed on ctx.
func deadlineOf(t *testing.T, interceptor connect.Interceptor, ctx context.Context) (time.Time, bool) {
	t.Helper()

	var gotDeadline time.Time
	var gotOK bool

	next := connect.UnaryFunc(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		gotDeadline, gotOK = ctx.Deadline()
		return connect.NewResponse(pb.PingResp_builder{}.Build()), nil
	})

	wrapped := interceptor.WrapUnary(next)
	if _, err := wrapped(ctx, connect.NewRequest(pb.PingReq_builder{}.Build())); err != nil {
		t.Fatalf("wrapped unary call: %v", err)
	}

	return gotDeadline, gotOK
}

// A call with no deadline must still be bounded.
func TestNewDeadlineInterceptorAppliesTheDefaultWhenTheCallerSetNone(t *testing.T) {
	const budget = 5 * time.Second
	interceptor := newDeadlineInterceptor(budget)

	before := time.Now()
	deadline, ok := deadlineOf(t, interceptor, context.Background())
	after := time.Now()

	if !ok {
		t.Fatal("no deadline was applied to a context that started with none")
	}

	// Anchored to "now + budget", bracketed by the calls around it.
	if deadline.Before(before.Add(budget)) || deadline.After(after.Add(budget)) {
		t.Errorf("deadline = %v, want within [%v, %v]", deadline, before.Add(budget), after.Add(budget))
	}
}

// A caller's tighter deadline must not be replaced.
func TestNewDeadlineInterceptorKeepsATighterCallerDeadline(t *testing.T) {
	interceptor := newDeadlineInterceptor(30 * time.Second)

	want := time.Now().Add(500 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()

	got, ok := deadlineOf(t, interceptor, ctx)
	if !ok {
		t.Fatal("the caller's deadline was dropped entirely")
	}
	if !got.Equal(want) {
		t.Errorf("deadline = %v, want the caller's own %v (unchanged)", got, want)
	}
}

// A caller's looser deadline must not be clamped down to the interceptor budget.
func TestNewDeadlineInterceptorKeepsALooserCallerDeadline(t *testing.T) {
	interceptor := newDeadlineInterceptor(1 * time.Second)

	want := time.Now().Add(1 * time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()

	got, ok := deadlineOf(t, interceptor, ctx)
	if !ok {
		t.Fatal("the caller's looser deadline was dropped entirely")
	}
	if !got.Equal(want) {
		t.Errorf("deadline = %v, want the caller's own looser %v (not clamped down to the interceptor's budget)", got, want)
	}
}

// fakeStreamingClientConn embeds the nil interface so an unexpected call panics.
type fakeStreamingClientConn struct {
	connect.StreamingClientConn
}

// The long-lived reverse stream must not have a call deadline imposed on it.
func TestNewDeadlineInterceptorWrapStreamingClientIsANoOp(t *testing.T) {
	interceptor := newDeadlineInterceptor(1 * time.Millisecond)

	var gotOK bool
	next := connect.StreamingClientFunc(func(ctx context.Context, _ connect.Spec) connect.StreamingClientConn {
		_, gotOK = ctx.Deadline()
		return &fakeStreamingClientConn{}
	})

	wrapped := interceptor.WrapStreamingClient(next)
	wrapped(context.Background(), connect.Spec{})

	if gotOK {
		t.Error("WrapStreamingClient imposed a deadline; a reverse action stream must stay open for its own lifetime, not the unary call budget")
	}
}

// A caller's deadline on a streaming call must survive untouched.
func TestNewDeadlineInterceptorWrapStreamingClientDoesNotTouchAnExistingDeadline(t *testing.T) {
	interceptor := newDeadlineInterceptor(1 * time.Millisecond)

	want := time.Now().Add(1 * time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()

	var got time.Time
	var gotOK bool
	next := connect.StreamingClientFunc(func(ctx context.Context, _ connect.Spec) connect.StreamingClientConn {
		got, gotOK = ctx.Deadline()
		return &fakeStreamingClientConn{}
	})

	interceptor.WrapStreamingClient(next)(ctx, connect.Spec{})

	if !gotOK || !got.Equal(want) {
		t.Errorf("deadline = %v (ok=%v), want the caller's own %v untouched", got, gotOK, want)
	}
}
