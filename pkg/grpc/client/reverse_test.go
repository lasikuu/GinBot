package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestMain(m *testing.M) {
	// The package logs unconditionally; give it a logger rather than a nil pointer.
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

func action(t *testing.T, clientAction pb.ClientAction) *pb.OpenClientActionStreamResp {
	t.Helper()
	content, err := structpb.NewStruct(map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("build struct: %v", err)
	}
	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &clientAction,
		Content:      content,
	}.Build()
}

// withReverseClient installs a client for one test and restores whatever was
// there. ReverseServiceClient is a package-level variable and several tests here
// write it, so an un-restored assignment leaks into whichever test runs next.
func withReverseClient(t *testing.T, c pb.ReverseServiceClient) {
	t.Helper()

	previous := ReverseServiceClient
	ReverseServiceClient = c
	t.Cleanup(func() { ReverseServiceClient = previous })
}

// observeLogs installs a recording logger for one test and restores the previous
// one.
//
// log.Z is a package global read without synchronisation by every goroutine this
// package starts, so the restore is not tidiness: an assignment that outlives its
// test is the write half of a data race against any reconnect loop or server
// handler still unwinding. Which is also why every test here that starts such a
// goroutine joins it before returning.
func observeLogs(t *testing.T) *observer.ObservedLogs {
	t.Helper()

	core, logs := observer.New(zapcore.WarnLevel)

	previousZ, previousS := log.Z, log.S
	log.Z = zap.New(core)
	log.S = log.Z.Sugar()
	t.Cleanup(func() {
		log.Z = previousZ
		log.S = previousS
	})

	return logs
}

// durationFields returns every time.Duration an entry carries.
//
// Read by value rather than by field name so the log line stays free to be
// reworded or its fields renamed; retry_in is the only Duration on this line, and
// what must not change is that the delay is recoverable from the record.
func durationFields(entry observer.LoggedEntry) []time.Duration {
	var durations []time.Duration
	for _, value := range entry.ContextMap() {
		if duration, ok := value.(time.Duration); ok {
			durations = append(durations, duration)
		}
	}

	return durations
}

// streamAttempt scripts one connection attempt: whether the stream opens, whether
// the registration message gets through, and how the stream then ends.
//
// All three are separate because the outcome runOnce reports — and so the delay
// the loop goes on to take — depends on which of them failed.
type streamAttempt struct {
	// openErr fails OpenClientActionStream outright: a server that is not there.
	openErr error
	// sendErr breaks the registration message on a stream that did open.
	sendErr error
	// recvErr is the stream's terminal status, which is what recvOutcome
	// classifies. Left unset it is io.EOF, an orderly close by the server.
	recvErr error
}

// scriptedReverseClient is the seam runOnce reaches the server through. It plays
// one streamAttempt per connection attempt, repeating the last entry once the
// script runs out.
//
// Repeating rather than exhausting is what lets a test describe "every attempt
// fails" as a single entry, and it means the number of attempts is bounded by the
// caller's context rather than by the length of the script — so a loop that runs
// away fails on the caller's deadline instead of on an index panic here.
type scriptedReverseClient struct {
	script []streamAttempt

	mu       sync.Mutex
	attempts int
}

// withScript installs a scripted client for one test and hands it back so the
// test can assert on how many attempts the loop made.
func withScript(t *testing.T, script ...streamAttempt) *scriptedReverseClient {
	t.Helper()

	client := &scriptedReverseClient{script: script}
	withReverseClient(t, client)

	return client
}

func (c *scriptedReverseClient) OpenClientActionStream(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp], error) {
	c.mu.Lock()
	attempt := c.script[min(c.attempts, len(c.script)-1)]
	c.attempts++
	c.mu.Unlock()

	if attempt.openErr != nil {
		return nil, attempt.openErr
	}

	// A fresh stream per attempt: actions are consumed as they are delivered, so
	// a shared one would replay a truncated script on the second reconnect.
	return &fakeClientStream{sendErr: attempt.sendErr, recvErr: attempt.recvErr}, nil
}

func (c *scriptedReverseClient) attemptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

// fakeClientStream is the client side of the bidi stream in memory. The embedded
// interface is left nil deliberately: only Send and Recv are part of what runOnce
// is being tested against, and a nil dereference on anything else is a louder
// failure than a silently plausible default.
type fakeClientStream struct {
	grpc.ClientStream

	// sendErr breaks the initial registration message.
	sendErr error

	mu   sync.Mutex
	sent []*pb.OpenClientActionStreamReq
	// actions are delivered in order, then recvErr ends the stream. recvErr is
	// what runOnce has to classify.
	actions []*pb.OpenClientActionStreamResp
	recvErr error
}

func (f *fakeClientStream) Send(req *pb.OpenClientActionStreamReq) error {
	if f.sendErr != nil {
		return f.sendErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, req)

	return nil
}

func (f *fakeClientStream) Recv() (*pb.OpenClientActionStreamResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.actions) > 0 {
		next := f.actions[0]
		f.actions = f.actions[1:]
		return next, nil
	}

	// io.EOF rather than the zero value of recvErr. runOnce's receive loop only
	// ends on an error, so (nil, nil) makes it dispatch nil messages forever —
	// and a test that populated actions and forgot the terminal error would hang
	// the suite for its whole timeout instead of failing. A stream always ends.
	if f.recvErr == nil {
		return nil, io.EOF
	}

	return nil, f.recvErr
}

func (f *fakeClientStream) Context() context.Context { return context.Background() }

func TestDispatchInvokesRegisteredHandler(t *testing.T) {
	var got *pb.OpenClientActionStreamResp
	calls := 0

	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(_ context.Context, in *pb.OpenClientActionStreamResp) {
			calls++
			got = in
		},
	}

	in := action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST)
	dispatch(context.Background(), in, handlers)

	if calls != 1 {
		t.Fatalf("handler called %d times, want 1", calls)
	}
	if got == nil {
		t.Fatal("handler received a nil action")
	}
	if got.GetContent().AsMap()["key"] != "value" {
		t.Errorf("handler received content %v, want key=value", got.GetContent().AsMap())
	}
}

// An action with no handler must not invoke an unrelated one, and must not panic.
func TestDispatchWithNoRegisteredHandler(t *testing.T) {
	called := false
	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			called = true
		},
	}

	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION), handlers)

	if called {
		t.Error("a handler registered for a different action was invoked")
	}
}

func TestDispatchWithEmptyRegistry(t *testing.T) {
	// Must not panic on a nil map.
	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), nil)
}

func TestDispatchRoutesEachActionToItsOwnHandler(t *testing.T) {
	testCalls, notifyCalls := 0, 0
	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			testCalls++
		},
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION: func(context.Context, *pb.OpenClientActionStreamResp) {
			notifyCalls++
		},
	}

	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), handlers)
	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION), handlers)
	dispatch(context.Background(), action(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION), handlers)

	if testCalls != 1 {
		t.Errorf("SEND_TEST handler called %d times, want 1", testCalls)
	}
	if notifyCalls != 2 {
		t.Errorf("SEND_NOTIFICATION handler called %d times, want 2", notifyCalls)
	}
}

// waitForAttempts blocks until the loop has opened at least want streams, so a
// test can act on the loop being parked in its wait rather than guessing.
func waitForAttempts(t *testing.T, client *scriptedReverseClient, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if client.attemptCount() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}

	t.Fatalf("the loop made %d connection attempts within the deadline, want at least %d",
		client.attemptCount(), want)
}

// RunClientActionStream must return on cancellation rather than sleeping out the
// delay it is parked in, so a client shutting down does not hold the process open
// for up to reconnectMaxBackoff.
//
// The version of this test that this replaces installed a nil
// ReverseServiceClient and claimed that made runOnce "fail immediately". It does
// not fail, it PANICS — a nil interface has no OpenClientActionStream to call —
// so the panic unwound out of the loop on the first iteration, the test's own
// recover() swallowed it, done closed, and the select this test is named after
// was never reached. Deleting the ctx.Done() arm outright still passed it.
//
// Hence the real client below, and hence no recover(): a panic here is a defect
// and must fail rather than be absorbed.
func TestRunClientActionStreamStopsOnContextCancel(t *testing.T) {
	// Fails to OPEN, so runOnce returns an error and the loop reaches its wait.
	client := withScript(t, streamAttempt{openErr: errors.New("connection refused")})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunClientActionStream(ctx, pb.Platform_PLATFORM_DISCORD, nil)
	}()

	// Cancelled only once a connection has actually been attempted. Cancelling
	// first would return through the ctx.Err() check at the top of the loop, a
	// different and much weaker path.
	waitForAttempts(t, client, 1)
	cancel()

	// Measured against the delay the loop is actually sitting in. "Returned
	// within 5 seconds" cannot tell a cancellation apart from the delay simply
	// elapsing, which is why the previous shape of this assertion proved nothing
	// even once the panic was gone.
	firstDelay := nextBackoff(reconnectMinBackoff, streamUnreachable)

	select {
	case <-done:
	case <-time.After(firstDelay / 2):
		t.Errorf("RunClientActionStream did not return within %v of cancellation; it is parked in a %v delay, so it is waiting that out instead of selecting on ctx.Done()",
			firstDelay/2, firstDelay)

		// Joined even on failure. A goroutine still inside the loop logs through
		// the package-global log.Z, which other tests in this file assign — the
		// exact race that leaking handlers caused here before.
		select {
		case <-done:
		case <-time.After(4 * firstDelay):
			t.Fatal("RunClientActionStream never returned at all")
		}
	}

	// One attempt, not two: a loop that waited its delay out and went round
	// again would have opened a second stream on the way past.
	if got := client.attemptCount(); got != 1 {
		t.Errorf("the loop made %d connection attempts after one cancellation, want 1", got)
	}
}

// ── The reconnect delays the loop actually waits ─────────────────────────────
//
// Every backoff assertion in this file used to be on nextBackoff in isolation,
// which is why the whole ladder shifted by one full step — nextBackoff moved from
// after the wait to before it — without a single test noticing. These drive the
// loop itself through its injected clock.

// recordingClock stands in for time.After inside runClientActionStream. It
// records the delay the loop asked for and hands back a channel that has already
// fired, so the schedule can be asserted at full speed.
type recordingClock struct {
	// stopAfter bounds the run: the context is cancelled once this many delays
	// have been recorded. Without it a loop that never terminates would spin at
	// full speed rather than fail.
	stopAfter int
	stop      func()

	mu     sync.Mutex
	delays []time.Duration
}

func (c *recordingClock) wait(delay time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.delays = append(c.delays, delay)
	reached := len(c.delays) >= c.stopAfter
	c.mu.Unlock()

	if reached {
		c.stop()
	}

	fired := make(chan time.Time, 1)
	fired <- time.Now()

	return fired
}

func (c *recordingClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.delays)
}

// driveReconnects runs the reconnect loop over a scripted sequence of outcomes
// and returns the delays it actually waited, in order.
func driveReconnects(t *testing.T, script []streamAttempt, delays int) []time.Duration {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := &recordingClock{stopAfter: delays, stop: cancel}
	withReverseClient(t, &scriptedReverseClient{script: script})

	done := make(chan struct{})
	go func() {
		defer close(done)
		runClientActionStream(ctx, pb.Platform_PLATFORM_DISCORD, nil, clock.wait)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// Nothing here waits on a real clock, so this deadline can only be hit
		// by a loop that ignores its context — which must fail rather than run
		// until the suite times out.
		t.Fatalf("runClientActionStream did not return after %d recorded delays; the loop is not bounded by its context", delays)
	}

	return clock.recorded()
}

// TestTheReconnectDelaysTheLoopWaits pins the schedule, exact values and all.
//
// The exact ladder rather than the properties, because the properties held
// throughout: the delays grew, they capped, and an established stream reset them.
// What changed unnoticed was WHICH delay each attempt got, and only the literal
// sequence catches that.
func TestTheReconnectDelaysTheLoopWaits(t *testing.T) {
	unreachable := streamAttempt{openErr: errors.New("connection refused")}
	dropped := streamAttempt{recvErr: status.Error(codes.Unavailable, "transport is closing")}
	refused := streamAttempt{recvErr: status.Error(codes.ResourceExhausted, "too many client action streams")}

	tests := []struct {
		name   string
		script []streamAttempt
		want   []time.Duration
	}{
		{
			// Starts at 2s, not the 1s reconnectMinBackoff: nextBackoff is
			// applied before the wait, so the first failure is already escalated
			// once by the time anything is waited. Accepted deliberately, and
			// pinned here so it cannot drift again in either direction.
			name:   "an unreachable server escalates to the cap and holds there",
			script: []streamAttempt{unreachable},
			want: []time.Duration{
				2 * time.Second,
				4 * time.Second,
				8 * time.Second,
				16 * time.Second,
				30 * time.Second,
				30 * time.Second,
			},
		},
		{
			// The ratchet. One healthy stream has to clear the accumulated
			// penalty on the NEXT delay, not the one after it, or a client that
			// dropped a few times over its lifetime waits the maximum before
			// every later reconnect however healthy the intervening connection
			// was.
			name:   "an established stream resets the very next delay",
			script: []streamAttempt{unreachable, unreachable, unreachable, dropped, unreachable, unreachable},
			want: []time.Duration{
				2 * time.Second,
				4 * time.Second,
				8 * time.Second,
				1 * time.Second,
				2 * time.Second,
				4 * time.Second,
			},
		},
		{
			// The defect this whole pass exists to fix, asserted where it bites.
			// A refusal arrives as the terminal status of a stream that DID open,
			// so classifying it as a healthy drop reset the delay and a client
			// locked out by the registry cap retried once a second for as long as
			// the lockout lasted. maxStreamClients' doc comment cites capped
			// backoff as the reason refusing at 64 is safe; this is what makes
			// that true.
			name:   "a server-side refusal escalates instead of resetting",
			script: []streamAttempt{refused},
			want: []time.Duration{
				2 * time.Second,
				4 * time.Second,
				8 * time.Second,
				16 * time.Second,
				30 * time.Second,
				30 * time.Second,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := driveReconnects(t, tc.script, len(tc.want))

			if !slices.Equal(got, tc.want) {
				t.Errorf("the loop waited %v, want %v", got, tc.want)
			}

			// Saturation, not overrun. Checked on every delay rather than on the
			// last, because a ladder that overshoots and is then clamped back
			// looks identical at the end.
			for i, delay := range got {
				if delay > reconnectMaxBackoff {
					t.Errorf("delay %d = %v, above the %v cap", i+1, delay, reconnectMaxBackoff)
				}
			}
		})
	}
}

// A refused client must never come back to the floor, whatever the ladder is
// retuned to. Stated separately from the exact schedule above so that a failure
// says "the refusal reset the backoff" rather than "delay 4 differs".
func TestARefusedClientsDelaysNeverReturnToTheFloor(t *testing.T) {
	refused := streamAttempt{recvErr: status.Error(codes.ResourceExhausted, "too many client action streams")}

	for i, delay := range driveReconnects(t, []streamAttempt{refused}, 6) {
		if delay <= reconnectMinBackoff {
			t.Errorf("delay %d of a refused client = %v, want it above the %v floor rather than reset to it; a refusal is not a healthy drop",
				i+1, delay, reconnectMinBackoff)
		}
	}
}

// retry_in is what an operator reads to decide whether a client is backing off
// at all, and having a single point where the backoff transitions is most of why
// it was moved before the wait. Split across the wait — a reset before it and an
// escalation after it, which is what this replaced — the two disagree exactly
// when it matters: the line said 8s while the loop went on to wait 1s, so a
// client that had just recovered read as one still deep in backoff.
//
// The script therefore has to contain a recovery. Escalating failures alone
// cannot tell the two orderings apart, because both log and wait the same value
// on the same iteration.
func TestTheLoggedRetryDelayIsTheDelayThenWaited(t *testing.T) {
	logs := observeLogs(t)

	unreachable := streamAttempt{openErr: errors.New("connection refused")}
	dropped := streamAttempt{recvErr: status.Error(codes.Unavailable, "transport is closing")}

	delays := driveReconnects(t, []streamAttempt{unreachable, unreachable, dropped, unreachable}, 4)

	entries := logs.TakeAll()
	if len(entries) != len(delays) {
		t.Fatalf("%d reconnects were logged for %d delays; every drop a client recovers from has to be visible",
			len(entries), len(delays))
	}

	for i, entry := range entries {
		if !slices.Contains(durationFields(entry), delays[i]) {
			t.Errorf("reconnect %d logged %v but then waited %v", i+1, entry.ContextMap(), delays[i])
		}
	}
}

// ── The reconnect backoff ────────────────────────────────────────────────────
//
// These assert nextBackoff itself. The test they replace defined a local closure
// mirroring RunClientActionStream's loop and asserted that, so it would have
// passed with the production transition deleted outright.
//
// They also assert properties — grows, saturates, resets — rather than the exact
// doubling. Reproducing the arithmetic here would be the same mistake in a
// smaller font, and it would mean the schedule could not be retuned without
// rewriting the suite.

// saturate drives nextBackoff with one outcome until it stops growing, checking
// on every step that the cap is never exceeded rather than only that the final
// value happens to equal it. maxSteps is a bound on a runaway loop, not a claim
// about how many escalations the schedule takes.
func saturate(t *testing.T, outcome streamOutcome) time.Duration {
	t.Helper()

	const maxSteps = 64

	backoff := reconnectMinBackoff
	for i := 0; i < maxSteps; i++ {
		next := nextBackoff(backoff, outcome)
		if next > reconnectMaxBackoff {
			t.Fatalf("nextBackoff returned %v at escalation %d, above the %v cap", next, i+1, reconnectMaxBackoff)
		}
		if next == backoff {
			return backoff
		}
		backoff = next
	}

	t.Fatalf("%s was still escalating after %d steps, want it capped at %v",
		outcome, maxSteps, reconnectMaxBackoff)

	return 0
}

// A client that cannot reach the server at all must not hammer it once a second
// forever, which is what a delay that never grows amounts to.
func TestBackoffEscalatesAndSaturatesWhenTheServerIsUnreachable(t *testing.T) {
	if got := nextBackoff(reconnectMinBackoff, streamUnreachable); got <= reconnectMinBackoff {
		t.Errorf("nextBackoff(%v, streamUnreachable) = %v, want a longer delay", reconnectMinBackoff, got)
	}

	if got := saturate(t, streamUnreachable); got != reconnectMaxBackoff {
		t.Errorf("repeated streamUnreachable settled at %v, want the %v cap", got, reconnectMaxBackoff)
	}

	if got := nextBackoff(reconnectMaxBackoff, streamUnreachable); got != reconnectMaxBackoff {
		t.Errorf("nextBackoff(%v, streamUnreachable) = %v, want it held at the cap", reconnectMaxBackoff, got)
	}
}

// Regression test for the backoff ratchet: the delay was initialised once
// outside the loop and only ever doubled, so a client that had dropped a few
// times waited the 30s maximum before every later reconnect no matter how
// healthy the intervening stream was. Only an established stream clears it.
func TestBackoffResetsAfterAnEstablishedStream(t *testing.T) {
	saturated := saturate(t, streamUnreachable)

	if got := nextBackoff(saturated, streamEstablished); got != reconnectMinBackoff {
		t.Errorf("nextBackoff(%v, streamEstablished) = %v, want %v", saturated, got, reconnectMinBackoff)
	}
	if got := nextBackoff(reconnectMinBackoff, streamEstablished); got != reconnectMinBackoff {
		t.Errorf("nextBackoff(%v, streamEstablished) = %v, want it to stay at the minimum",
			reconnectMinBackoff, got)
	}
}

// The registry cap refuses a client with ResourceExhausted, and that error
// arrives from Recv AFTER the stream was established. Read as a healthy
// connection it RESET the delay, so a refused client retried once per second
// indefinitely — the precise hammering that maxStreamClients' doc comment cites
// capped backoff as the reason it is safe to refuse. This test is what makes
// that claim true.
func TestBackoffEscalatesOnAServerSideRejection(t *testing.T) {
	first := nextBackoff(reconnectMinBackoff, streamRejected)
	if first == reconnectMinBackoff {
		t.Fatalf("nextBackoff(%v, streamRejected) pinned at the minimum; a refused client would retry every %v forever",
			reconnectMinBackoff, reconnectMinBackoff)
	}
	if first < reconnectMinBackoff {
		t.Errorf("nextBackoff(%v, streamRejected) = %v, want a longer delay", reconnectMinBackoff, first)
	}

	if got := saturate(t, streamRejected); got != reconnectMaxBackoff {
		t.Errorf("repeated streamRejected settled at %v, want the %v cap", got, reconnectMaxBackoff)
	}
}

// ── How runOnce classifies the end of a stream ───────────────────────────────

// TestRunOnceClassifiesAnEndedStream: the whole backoff decision hangs on this
// one value, so a code sorted into the wrong bucket is a live incident rather
// than a cosmetic mislabelling.
func TestRunOnceClassifiesAnEndedStream(t *testing.T) {
	tests := []struct {
		name    string
		recvErr error
		want    streamOutcome
	}{
		// The registry cap's refusal. Sorted as established it resets the delay,
		// which is what made a refused client retry once a second.
		{"registry at capacity", status.Error(codes.ResourceExhausted, "too many client action streams"), streamRejected},
		// A refusal on the merits. Retrying hard will not change the answer.
		{"caller refused", status.Error(codes.PermissionDenied, "not a platform client"), streamRejected},
		// An older peer, or one that never registered ReverseService at all.
		{"stream not served", status.Error(codes.Unimplemented, "unknown service"), streamRejected},
		// A genuine drop: the connection worked, so the next attempt should not
		// be penalised for the last one.
		{"transport dropped", status.Error(codes.Unavailable, "transport is closing"), streamEstablished},
		{"server closed the stream", io.EOF, streamEstablished},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withScript(t, streamAttempt{recvErr: tc.recvErr})

			got, err := runOnce(context.Background(), pb.Platform_PLATFORM_DISCORD, nil)
			if got != tc.want {
				t.Errorf("runOnce classified %v as %s (err %v), want %s",
					tc.recvErr, got, err, tc.want)
			}
		})
	}
}

// A server that is down never established anything, so the attempt must be
// penalised rather than treated as a healthy connection that happened to drop.
func TestRunOnceReportsAnUnreachableServer(t *testing.T) {
	openErr := errors.New("connection refused")
	withScript(t, streamAttempt{openErr: openErr})

	got, err := runOnce(context.Background(), pb.Platform_PLATFORM_DISCORD, nil)
	if got != streamUnreachable {
		t.Errorf("runOnce = %s, want streamUnreachable", got)
	}
	if !errors.Is(err, openErr) {
		t.Errorf("runOnce error = %v, want the open error %v", err, openErr)
	}
}

// The registration message is what makes the stream usable at all — without it
// the server does not know which platform to route here — so a stream that
// opened but could not carry it was never established either.
func TestRunOnceReportsAFailedRegistrationAsUnreachable(t *testing.T) {
	sendErr := errors.New("broken pipe")
	withScript(t, streamAttempt{sendErr: sendErr})

	got, err := runOnce(context.Background(), pb.Platform_PLATFORM_DISCORD, nil)
	if got != streamUnreachable {
		t.Errorf("runOnce = %s, want streamUnreachable", got)
	}
	if !errors.Is(err, sendErr) {
		t.Errorf("runOnce error = %v, want the send error %v", err, sendErr)
	}
}

// ── A panicking action handler ───────────────────────────────────────────────

// dispatchNoPanic calls dispatch and reports an escaping panic as an ordinary
// test failure. Without the recover here a regression takes the whole test
// binary down — which is exactly what it does to the client process in
// production, and exactly why it is worth asserting.
func dispatchNoPanic(t *testing.T, in *pb.OpenClientActionStreamResp, handlers ActionHandlers) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("a handler panic escaped dispatch: %v", r)
		}
	}()

	dispatch(context.Background(), in, handlers)
}

// dispatch calls handlers inline on the receive loop with no recover above it
// anywhere, so a single malformed action used to kill the whole client process
// instead of failing one delivery.
func TestDispatchSurvivesAPanickingHandler(t *testing.T) {
	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			panic("handler blew up")
		},
	}

	dispatchNoPanic(t, action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), handlers)
}

// A recover that leaves something wedged is barely better than the panic. The
// failure must stay confined to its own delivery: the next action still gets
// handled, and repeating the bad one does not degrade further.
func TestDispatchStillDeliversAfterAPanickingHandler(t *testing.T) {
	notified := 0
	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			panic("handler blew up")
		},
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION: func(context.Context, *pb.OpenClientActionStreamResp) {
			notified++
		},
	}

	const rounds = 3
	for i := 0; i < rounds; i++ {
		dispatchNoPanic(t, action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), handlers)
		dispatchNoPanic(t, action(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION), handlers)
	}

	if notified != rounds {
		t.Errorf("the healthy handler ran %d times across %d rounds, want %d", notified, rounds, rounds)
	}
}

// A silently swallowed panic is a defect that never gets diagnosed: the action
// vanishes and nothing anywhere says why. Asserted on level and on the recovered
// value surviving into the record, not on the wording or the field names, so the
// message stays free to change.
func TestDispatchReportsARecoveredPanic(t *testing.T) {
	const panicValue = "handler blew up"

	logs := observeLogs(t)

	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			panic(panicValue)
		},
	}

	dispatchNoPanic(t, action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), handlers)

	if logs.Len() == 0 {
		t.Fatal("a recovered handler panic produced no log entry at warn level or above")
	}

	// "Something panicked" with the value dropped is not much better than
	// silence: it is the value that says which handler to go and look at.
	if recorded := fmt.Sprint(logs.All()); !strings.Contains(recorded, panicValue) {
		t.Errorf("the recovered panic value %q is absent from the log record: %s", panicValue, recorded)
	}
}

// ── The refusal, end to end ──────────────────────────────────────────────────
//
// The two halves are asserted separately elsewhere: pkg/grpc/server asserts that
// a full registry refuses with ResourceExhausted, and
// TestRunOnceClassifiesAnEndedStream asserts that ResourceExhausted is a
// rejection. Neither is worth much alone — the classification is only correct if
// it matches the code the server actually puts on the wire — so this ties them
// together over a real transport.

// registryProbeCeiling bounds the fill loop. maxStreamClients is unexported in
// pkg/grpc/server, so the cap is discovered by hitting it rather than assumed;
// this exists only so a cap that is not enforced fails the test instead of
// hanging it.
const registryProbeCeiling = 256

// refusalGrace bounds how long fillServerRegistry waits for a refusal before
// treating the stream it just opened as admitted.
//
// There is no positive admission signal on the wire — an admitted client is
// simply one that was not refused — so admission has to be inferred from the
// absence of a refusal, and inferring it takes a wait. That wait is paid once per
// admitted stream, so the value is a real cost: it is roughly maxStreamClients
// times this, or about a third of a second, on the whole package's runtime.
//
// 5ms against a measured sub-millisecond round trip over an in-process bufconn
// under -race. Too short would not cause a false pass — the next iteration blocks
// on its own refusal just the same — only a few extra probe streams, which
// registryProbeCeiling bounds.
const refusalGrace = 5 * time.Millisecond

// gracefulStopDeadline bounds the teardown below so a regression in it fails this
// test rather than hanging the whole suite for its default timeout.
//
// Draining 64 handlers this way measures at about 2ms, so this is three orders of
// magnitude of headroom. It is deliberately not tighter: the failure mode being
// bounded is a deadlock, which no amount of waiting resolves, so the only thing a
// tighter bound buys is a flakier test on a loaded machine.
const gracefulStopDeadline = 10 * time.Second

// fillServerRegistry opens streams until the server refuses one, keeping every
// admitted stream open for the rest of the test. The refused stream never
// occupied a slot, so the registry is still full when this returns.
//
// The refusal is BLOCKED on after each open rather than checked at the top of the
// next iteration. Polling it non-blockingly meant nothing waited for the refusal
// goroutine to be scheduled, so the loop opened an indeterminate number of extra
// streams past the cap before noticing — with only registryProbeCeiling minus the
// cap of slack, and an overshoot surfacing as a fatal claiming the cap is not
// enforced over the wire, which is not the defect that happened.
func fillServerRegistry(t *testing.T, ctx context.Context, reverse pb.ReverseServiceClient) {
	t.Helper()

	registration := pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()

	for i := 0; i < registryProbeCeiling; i++ {
		stream, err := reverse.OpenClientActionStream(ctx)
		if err != nil {
			t.Fatalf("open stream %d: %v", i, err)
		}
		if err := stream.Send(registration); err != nil {
			t.Fatalf("register stream %d: %v", i, err)
		}

		// An admitted stream blocks in Recv for the rest of the test, holding its
		// slot exactly as a connected platform client does. A refused one reports
		// its terminal status here. The channel is buffered so this goroutine
		// cannot be left parked on a send once the loop has moved on.
		ended := make(chan error, 1)
		go func() {
			for {
				if _, err := stream.Recv(); err != nil {
					ended <- err
					return
				}
			}
		}()

		select {
		case err := <-ended:
			if status.Code(err) == codes.ResourceExhausted {
				return
			}
			t.Fatalf("stream %d ended with %v; want it either held open (admitted) or refused with ResourceExhausted",
				i, err)
		case <-time.After(refusalGrace):
		}
	}

	t.Fatalf("the server admitted %d streams without refusing one; the registry cap is not enforced over the wire",
		registryProbeCeiling)
}

// TestARefusedClientBacksOffRatherThanRetryingEverySecond drives a real
// ReverseServer to its cap and asserts the refused client's next delay grows.
//
// Its teardown carries a second, unrelated assertion, because nothing else in the
// repository covers it: see the cleanup below.
func TestARefusedClientBacksOffRatherThanRetryingEverySecond(t *testing.T) {
	// Registered before the teardown so it runs AFTER it. The teardown depends on
	// the admitted streams still being live: a handler whose stream context was
	// already cancelled returns on its own, which would make ReverseServer.
	// Shutdown() a no-op and quietly void the ordering assertion below.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	grpcServer := grpc.NewServer()
	// Kept in a variable rather than inlined into the Register call, because the
	// teardown has to reach it.
	reverseServer := server.NewReverseServer()
	pb.RegisterReverseServiceServer(grpcServer, reverseServer)

	listener := bufconn.Listen(1024 * 1024)
	served := make(chan struct{})
	go func() {
		defer close(served)
		// Serve returns ErrServerStopped on Stop, which is the normal path here.
		_ = grpcServer.Serve(listener)
	}()

	// passthrough is required: grpc.NewClient defaults to the DNS resolver, and
	// "bufnet" is not a hostname.
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		// This ordering is cmd/ginbot-server/main.go's, and this cleanup is the
		// only thing in the repository that exercises it end to end.
		//
		// It is load-bearing, not stylistic. GracefulStop waits for every handler
		// to return before it closes transports, and a reverse-stream handler
		// parked waiting for a client message never returns on its own — so
		// GracefulStop first DEADLOCKS, until the go test timeout here and until
		// the container runtime SIGKILLs the container in production. Shutdown
		// first releases the handlers, giving GracefulStop something that
		// finishes. If that ordering regresses, this cleanup is what hangs.
		//
		// Draining also fixes what this test used to do to the rest of the
		// package. It tore down with Stop(), and grpc-go's Server.stop waits on
		// handlersWG only when stopping gracefully or when WaitForHandlers(true)
		// was set — so Stop() returned while all 64 handlers admitted by
		// fillServerRegistry were still unwinding through deregister(), which
		// logs through the package-global log.Z that the next test in this file
		// assigns. A deterministic data race at -count=2, and the reason
		// -count=5 failed. WaitForHandlers(true) would silence it too, and prove
		// nothing.
		reverseServer.Shutdown()

		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			grpcServer.GracefulStop()
		}()

		select {
		case <-stopped:
		case <-time.After(gracefulStopDeadline):
			t.Errorf("GracefulStop did not finish within %v of Shutdown; the reverse-stream handlers are not being released",
				gracefulStopDeadline)
			// Forced, so a failure here is one failing test rather than a suite
			// that hangs until its default timeout.
			grpcServer.Stop()
			<-stopped
		}

		if err := conn.Close(); err != nil {
			t.Errorf("close client connection: %v", err)
		}
		<-served
	})

	reverse := pb.NewReverseServiceClient(conn)
	fillServerRegistry(t, ctx, reverse)

	withReverseClient(t, reverse)

	// A bounded context so a probe that is wrongly ADMITTED fails this test
	// rather than blocking in Recv until the whole suite times out.
	probeCtx, cancelProbe := context.WithTimeout(ctx, 10*time.Second)
	defer cancelProbe()

	outcome, err := runOnce(probeCtx, pb.Platform_PLATFORM_DISCORD, nil)
	if outcome != streamRejected {
		t.Fatalf("a client refused by a full registry was classified %s (err %v), want streamRejected",
			outcome, err)
	}

	if got := nextBackoff(reconnectMinBackoff, outcome); got <= reconnectMinBackoff {
		t.Errorf("a refused client's next delay = %v, want it longer than %v rather than pinned there",
			got, reconnectMinBackoff)
	}
}
