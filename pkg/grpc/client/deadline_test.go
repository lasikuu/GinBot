package client

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// deadlineOf runs req through interceptor's WrapUnary and reports the deadline
// next actually observed on ctx, so every case below is driven through the
// real interceptor chain rather than against a hand-rolled stand-in for it.
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

// TestNewDeadlineInterceptorAppliesTheDefaultWhenTheCallerSetNone is the
// ordinary case: a caller that dials out with context.Background() (or any
// context carrying no deadline at all) must still be bounded, or a wedged
// connection blocks the call forever.
func TestNewDeadlineInterceptorAppliesTheDefaultWhenTheCallerSetNone(t *testing.T) {
	const budget = 5 * time.Second
	interceptor := newDeadlineInterceptor(budget)

	before := time.Now()
	deadline, ok := deadlineOf(t, interceptor, context.Background())
	after := time.Now()

	if !ok {
		t.Fatal("no deadline was applied to a context that started with none")
	}

	// The deadline has to be anchored to roughly "now + budget", not to some
	// unrelated fixed point — bracketed between the call made just before it
	// and just after, plus the budget.
	if deadline.Before(before.Add(budget)) || deadline.After(after.Add(budget)) {
		t.Errorf("deadline = %v, want within [%v, %v]", deadline, before.Add(budget), after.Add(budget))
	}
}

// TestNewDeadlineInterceptorKeepsATighterCallerDeadline: a caller that already
// asked for less time than the interceptor's own budget must not have that
// tightened further by an interceptor call that runs before its own budget is
// even reached — that would silently shrink whatever timeout the caller
// deliberately chose.
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

// TestNewDeadlineInterceptorKeepsALooserCallerDeadline is the case the
// specification calls out as needing an explicit decision: a caller-supplied
// deadline is NEVER replaced, not even when it is more generous than the
// interceptor's own default budget. A batch job or a slow admin command that
// deliberately asks for five minutes must keep those five minutes rather than
// being clamped down to defaultCallTimeout.
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

// fakeStreamingClientConn is a connect.StreamingClientConn whose methods are
// never meant to be called in this test — WrapStreamingClient's job here is
// only to decide what ctx reaches `next`, so nothing beyond that seam is
// exercised. Embedding the nil interface makes an unexpected call panic loudly
// rather than silently returning a zero value.
type fakeStreamingClientConn struct {
	connect.StreamingClientConn
}

// TestNewDeadlineInterceptorWrapStreamingClientIsANoOp: a reverse action
// stream is long-lived by design (it is held open for the lifetime of the
// platform client), so imposing defaultCallTimeout on it would kill a healthy
// stream on a schedule that has nothing to do with its health. This is the
// one place newDeadlineInterceptor must NOT act.
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

// TestNewDeadlineInterceptorWrapStreamingClientDoesNotTouchAnExistingDeadline
// covers the same no-op from the other side: a caller-supplied deadline on a
// streaming call must also survive untouched, not just be left un-added-to.
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
