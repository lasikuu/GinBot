package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestMain(m *testing.M) {
	// The package logs unconditionally; give it a logger rather than a nil pointer.
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// actionReminderID is the id every action built by action() carries, so a
// dispatch test can assert the payload the handler received is the one that was
// sent rather than merely non-nil.
const actionReminderID = "0192f000-0000-7000-8000-000000000001"

// action builds a server-pushed action carrying a populated reminder_delivery
// arm.
//
// The arm is set rather than left empty because dispatch's job is to hand the
// WHOLE message to the handler untouched: with no payload at all, a dispatch
// that passed a freshly built message instead of the received one would still
// satisfy every assertion here.
func action(t *testing.T, clientAction pb.ClientAction) *pb.OpenClientActionStreamResp {
	t.Helper()

	reminderID := actionReminderID

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &clientAction,
		ReminderDelivery: pb.ReminderDelivery_builder{
			ReminderId: &reminderID,
		}.Build(),
	}.Build()
}

// actionWithoutPayload builds an action whose oneof arm is UNSET, which is a
// representable message and therefore an input dispatch has to carry.
func actionWithoutPayload(t *testing.T, clientAction pb.ClientAction) *pb.OpenClientActionStreamResp {
	t.Helper()

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &clientAction,
	}.Build()
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

// testIdentity is the StreamIdentity every reconnect-loop test in this file
// drives the loop with. Below runOnce's ensure/open seam, nothing but a stubbed
// ensure func or a real ensureRegistered call ever reads these fields, so a
// single fixed value is enough everywhere the ensure func itself is faked out
// directly.
var testIdentity = StreamIdentity{
	Platform:    pb.Platform_PLATFORM_DISCORD,
	PlatformUID: "caller-uid",
	Username:    "caller",
}

// alwaysEnsure is an `ensure` that always succeeds, standing in for a
// successful ensureRegistered call in every test that is not itself about
// registration.
func alwaysEnsure(context.Context) error { return nil }

// waitForCount blocks until count() reports at least want, so a test can act on
// the loop being parked in its wait rather than guessing.
func waitForCount(t *testing.T, count func() int, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if count() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}

	t.Fatalf("count reached %d within the deadline, want at least %d", count(), want)
}

// fakeActionStream is the in-memory double for actionStream — the seam runOnce
// reaches the transport through now that Connect's own
// *connect.BidiStreamForClient cannot be constructed outside
// connectrpc.com/connect (its only constructor is reached by dialing a real
// HTTP endpoint; see reverse_h2c_test.go for the one test in this package that
// does that instead of faking this interface).
type fakeActionStream struct {
	// sendErr breaks the registration message on a stream that did open.
	sendErr error

	mu   sync.Mutex
	sent []*pb.OpenClientActionStreamReq
	// actions are delivered in order, then recvErr ends the stream. recvErr is
	// what runOnce has to classify.
	actions []*pb.OpenClientActionStreamResp
	recvErr error

	closeRequestCalls  int
	closeResponseCalls int
}

func (f *fakeActionStream) Send(req *pb.OpenClientActionStreamReq) error {
	if f.sendErr != nil {
		return f.sendErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, req)

	return nil
}

func (f *fakeActionStream) Receive() (*pb.OpenClientActionStreamResp, error) {
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

func (f *fakeActionStream) CloseRequest() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeRequestCalls++
	return nil
}

func (f *fakeActionStream) CloseResponse() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeResponseCalls++
	return nil
}

func (f *fakeActionStream) sentMessages() []*pb.OpenClientActionStreamReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// streamAttempt scripts one connection attempt: whether the registration
// message gets through, and how the stream then ends.
//
// There is deliberately no field for "the open call itself failed". Connect's
// OpenClientActionStream(ctx) returns a *connect.BidiStreamForClient directly
// with no error at all — the stream is opened lazily, and a transport failure
// only ever surfaces later, from Send or Receive. That is the one seam the move
// to connectrpc.com/connect actually changed here: streamUnreachable is now
// reached only through ensureRegistered failing (before open is even attempted)
// or through Send failing on a stream that did open. See
// TestRunOnceReportsAnUnreachableServerWhenEnsureRegisteredFails and
// TestRunOnceReportsAnUnreachableServerWhenSendFails.
type streamAttempt struct {
	// sendErr breaks the registration message on a stream that did open.
	sendErr error
	// recvErr is the stream's terminal status, which is what recvOutcome
	// classifies. Left unset it is io.EOF, an orderly close by the server.
	recvErr error
	// actions are delivered by Receive, in order, before recvErr ends the
	// stream.
	actions []*pb.OpenClientActionStreamResp
}

// scriptedStreams is the seam runOnce reaches the server through. It plays one
// streamAttempt per connection attempt, repeating the last entry once the
// script runs out.
//
// Repeating rather than exhausting is what lets a test describe "every attempt
// fails" as a single entry, and it means the number of attempts is bounded by
// the caller's context rather than by the length of the script — so a loop
// that runs away fails on the caller's deadline instead of on an index panic
// here.
type scriptedStreams struct {
	script []streamAttempt

	mu       sync.Mutex
	attempts int
	streams  []*fakeActionStream
}

// opener returns an actionStreamOpener backed by this script. A fresh
// *fakeActionStream is returned per call: actions are consumed as they are
// delivered, so a shared stream would replay a truncated script on the second
// reconnect.
func (s *scriptedStreams) opener() actionStreamOpener {
	return func(context.Context) actionStream {
		s.mu.Lock()
		attempt := s.script[min(s.attempts, len(s.script)-1)]
		s.attempts++
		stream := &fakeActionStream{sendErr: attempt.sendErr, recvErr: attempt.recvErr, actions: attempt.actions}
		s.streams = append(s.streams, stream)
		s.mu.Unlock()

		return stream
	}
}

func (s *scriptedStreams) attemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

func (s *scriptedStreams) lastStream() *fakeActionStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streams) == 0 {
		return nil
	}
	return s.streams[len(s.streams)-1]
}

// countingEnsure is a scriptable `ensure` func: it always reports how many
// times it was called, and either always succeeds or always fails with err.
type countingEnsure struct {
	err error

	mu    sync.Mutex
	calls int
}

func (c *countingEnsure) fn() func(context.Context) error {
	return func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.calls++
		return c.err
	}
}

func (c *countingEnsure) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// ── dispatch ──────────────────────────────────────────────────────────────────

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
	// The arm AND its contents. dispatch routes on client_action alone and never
	// looks inside the payload, so what is asserted here is that it hands the
	// message through intact — the handler is the only thing entitled to decide
	// what the arm means.
	if !got.HasReminderDelivery() {
		t.Fatalf("handler received payload arm %v, want reminder_delivery", got.WhichPayload())
	}
	if id := got.GetReminderDelivery().GetReminderId(); id != actionReminderID {
		t.Errorf("handler received reminder id %q, want %q", id, actionReminderID)
	}
}

// TestDispatchDeliversAnActionWithNoPayloadArm.
//
// A oneof can be unset, so this is a well-formed message rather than a corrupt
// one, and dispatch has to deliver it like any other: it is the HANDLER's job to
// decide that an unset arm is unusable, and it cannot do that for an action
// dispatch dropped or panicked on.
//
// dispatch's own recover() does not make this test redundant. A recovered panic
// is still a lost delivery and a stack trace in the log, and the point here is
// that there is nothing to recover from.
func TestDispatchDeliversAnActionWithNoPayloadArm(t *testing.T) {
	var got *pb.OpenClientActionStreamResp
	calls := 0

	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION: func(_ context.Context, in *pb.OpenClientActionStreamResp) {
			calls++
			got = in
		},
	}

	in := actionWithoutPayload(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION)
	if in.HasPayload() {
		t.Fatal("the fixture set a payload arm; this test is about an action with none")
	}

	// No recover() installed: a panic escaping dispatch fails the test here, and
	// takes the whole client process down in production.
	dispatch(context.Background(), in, handlers)

	if calls != 1 {
		t.Fatalf("handler called %d times for an action with no payload arm, want 1", calls)
	}
	if got == nil {
		t.Fatal("handler received a nil action")
	}
	if got.WhichPayload() != pb.OpenClientActionStreamResp_Payload_not_set_case {
		t.Errorf("handler received payload arm %v, want it still unset", got.WhichPayload())
	}
	if got.GetReminderDelivery() != nil {
		t.Error("an unset arm produced a non-nil reminder_delivery")
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
// Hence the scripted ensure func below, and hence no recover(): a panic here is
// a defect and must fail rather than be absorbed. ensureRegistered failing
// (rather than a stream failing to open) is what drives runOnce to return an
// error without ever calling open — Connect's OpenClientActionStream(ctx) has
// no error return of its own to fail with.
func TestRunClientActionStreamStopsOnContextCancel(t *testing.T) {
	ensure := &countingEnsure{err: errors.New("registration refused")}
	streams := &scriptedStreams{}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		runClientActionStream(ctx, streams.opener(), ensure.fn(), testIdentity, nil, time.After)
	}()

	// Cancelled only once an attempt has actually been made. Cancelling first
	// would return through the ctx.Err() check at the top of the loop, a
	// different and much weaker path.
	waitForCount(t, ensure.callCount, 1)
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
	// again would have called ensure a second time on the way past.
	if got := ensure.callCount(); got != 1 {
		t.Errorf("ensureRegistered was attempted %d times after one cancellation, want 1", got)
	}
	// And no stream was ever opened: ensureRegistered failing must short-circuit
	// before runOnce reaches open at all.
	if got := streams.attemptCount(); got != 0 {
		t.Errorf("a stream was opened %d times despite ensureRegistered failing first", got)
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

// driveReconnects runs the reconnect loop over a scripted sequence of stream
// outcomes (registration always succeeds via alwaysEnsure) and returns the
// delays it actually waited, in order.
func driveReconnects(t *testing.T, script []streamAttempt, delays int) []time.Duration {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := &recordingClock{stopAfter: delays, stop: cancel}
	streams := &scriptedStreams{script: script}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runClientActionStream(ctx, streams.opener(), alwaysEnsure, testIdentity, nil, clock.wait)
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
	// unreachable: the stream "opened" (fakeActionStream always does — Connect's
	// OpenClientActionStream has no error return of its own) but the
	// registration Send failed, exactly as a connection that never really came
	// up would.
	unreachable := streamAttempt{sendErr: errors.New("connection refused")}
	dropped := streamAttempt{recvErr: connect.NewError(connect.CodeUnavailable, errors.New("transport is closing"))}
	refused := streamAttempt{recvErr: connect.NewError(connect.CodeResourceExhausted, errors.New("too many client action streams"))}

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
	refused := streamAttempt{recvErr: connect.NewError(connect.CodeResourceExhausted, errors.New("too many client action streams"))}

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

	unreachable := streamAttempt{sendErr: errors.New("connection refused")}
	dropped := streamAttempt{recvErr: connect.NewError(connect.CodeUnavailable, errors.New("transport is closing"))}

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
// These assert nextBackoff itself, independent of any client seam.

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
		{"registry at capacity", connect.NewError(connect.CodeResourceExhausted, errors.New("too many client action streams")), streamRejected},
		// A refusal on the merits. Retrying hard will not change the answer.
		{"caller refused", connect.NewError(connect.CodePermissionDenied, errors.New("not a platform client")), streamRejected},
		// An older peer, or one that never registered ReverseService at all.
		{"stream not served", connect.NewError(connect.CodeUnimplemented, errors.New("unknown service")), streamRejected},
		// A caller resolved but not yet fit to hold a stream — e.g. not
		// registered. Retrying sooner does not change that either.
		{"caller not yet registered", connect.NewError(connect.CodeFailedPrecondition, errors.New("caller is not registered")), streamRejected},
		// A genuine drop: the connection worked, so the next attempt should not
		// be penalised for the last one.
		{"transport dropped", connect.NewError(connect.CodeUnavailable, errors.New("transport is closing")), streamEstablished},
		{"server closed the stream", io.EOF, streamEstablished},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			streams := &scriptedStreams{script: []streamAttempt{{recvErr: tc.recvErr}}}

			got, err := runOnce(context.Background(), streams.opener(), alwaysEnsure, testIdentity, nil)
			if got != tc.want {
				t.Errorf("runOnce classified %v as %s (err %v), want %s",
					tc.recvErr, got, err, tc.want)
			}
		})
	}
}

// A registration that never gets the chance to reach the server — because
// ensureRegistered refused it first — never established anything, so the
// attempt must be penalised rather than treated as a healthy connection that
// happened to drop. This is the first of the two ways streamUnreachable is
// reached now that OpenClientActionStream itself cannot fail to "open".
func TestRunOnceReportsAnUnreachableServerWhenEnsureRegisteredFails(t *testing.T) {
	ensureErr := errors.New("registration refused")
	ensure := &countingEnsure{err: ensureErr}
	streams := &scriptedStreams{}

	got, err := runOnce(context.Background(), streams.opener(), ensure.fn(), testIdentity, nil)
	if got != streamUnreachable {
		t.Errorf("runOnce = %s, want streamUnreachable", got)
	}
	if !errors.Is(err, ensureErr) {
		t.Errorf("runOnce error = %v, want the ensure error %v", err, ensureErr)
	}
	if got := streams.attemptCount(); got != 0 {
		t.Errorf("a stream was opened %d times despite ensureRegistered failing first; runOnce must check registration before opening", got)
	}
}

// The registration message is what makes the stream usable at all — without it
// the server does not know which platform to route here — so a stream that
// opened but could not carry it was never established either. This is the
// second of the two ways streamUnreachable is reached.
func TestRunOnceReportsAnUnreachableServerWhenSendFails(t *testing.T) {
	sendErr := errors.New("broken pipe")
	streams := &scriptedStreams{script: []streamAttempt{{sendErr: sendErr}}}

	got, err := runOnce(context.Background(), streams.opener(), alwaysEnsure, testIdentity, nil)
	if got != streamUnreachable {
		t.Errorf("runOnce = %s, want streamUnreachable", got)
	}
	if !errors.Is(err, sendErr) {
		t.Errorf("runOnce error = %v, want the send error %v", err, sendErr)
	}
}

// TestRunOnceSendsARegistrationCarryingTheIdentityPlatform: the registration
// message is what tells the server which platform this stream serves, so it
// has to carry the identity runOnce was actually given, not some other value.
func TestRunOnceSendsARegistrationCarryingTheIdentityPlatform(t *testing.T) {
	streams := &scriptedStreams{script: []streamAttempt{{}}}

	// A script entry with no recvErr and no actions ends the stream with
	// io.EOF, which runOnce reports as streamEstablished but still returns a
	// non-nil error for ("server closed the action stream") — an orderly
	// close is still an event worth logging. Only the outcome matters here.
	outcome, _ := runOnce(context.Background(), streams.opener(), alwaysEnsure, testIdentity, nil)
	if outcome != streamEstablished {
		t.Fatalf("runOnce = %s, want streamEstablished for an orderly close", outcome)
	}

	stream := streams.lastStream()
	if stream == nil {
		t.Fatal("no stream was opened")
	}
	sent := stream.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("%d registration messages were sent, want exactly 1", len(sent))
	}
	if got := sent[0].GetPlatformEnum(); got != testIdentity.Platform {
		t.Errorf("registration platform_enum = %v, want %v", got, testIdentity.Platform)
	}
}

// ── ensureRegistered ─────────────────────────────────────────────────────────

// fakeUserClient is a ginbotv1connect.UserServiceClient double whose only
// implemented method is Register; anything else calling through the embedded
// nil interface panics loudly rather than returning a zero value.
type fakeUserClient struct {
	ginbotv1connect.UserServiceClient

	err error

	mu    sync.Mutex
	calls int
}

func (f *fakeUserClient) Register(_ context.Context, _ *connect.Request[pb.RegisterReq]) (*connect.Response[pb.RegisterResp], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++

	if f.err != nil {
		return nil, f.err
	}

	id := "0192f000-0000-7000-8000-0000000000aa"
	return connect.NewResponse(pb.RegisterResp_builder{UserId: &id}.Build()), nil
}

func (f *fakeUserClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestEnsureRegisteredTreatsAlreadyExistsAsSuccess: a client whose account was
// created on a previous run must not treat that as a failure to reconnect over.
func TestEnsureRegisteredTreatsAlreadyExistsAsSuccess(t *testing.T) {
	fake := &fakeUserClient{err: connect.NewError(connect.CodeAlreadyExists, errors.New("this platform identity is already registered"))}
	c := &Clients{User: fake}

	if err := c.ensureRegistered(context.Background(), testIdentity); err != nil {
		t.Fatalf("ensureRegistered = %v, want nil for CodeAlreadyExists", err)
	}
	if got := fake.callCount(); got != 1 {
		t.Errorf("Register called %d times, want 1", got)
	}
}

// TestEnsureRegisteredRunsOnEveryAttempt pins the deliberate absence of a
// "already registered" latch.
//
// The latch is the obvious optimisation and an earlier version had one. It is
// wrong, and the failure it produces is silent and permanent: the account can
// go away underneath a long-lived client — `docker compose down -v`, a
// database restore, an operator deleting the row — after which the server
// refuses the stream with FailedPrecondition, recvOutcome correctly stops the
// reconnect loop hot-looping, and a latched client never re-registers. This
// was reproduced against a real server: four stream refusals, one Register
// call, backoff pinned at its 30s ceiling, reminder delivery dead until
// someone restarted the process.
//
// Re-registering costs one unary call per stream ATTEMPT, and attempts only
// happen when the stream drops.
func TestEnsureRegisteredRunsOnEveryAttempt(t *testing.T) {
	fake := &fakeUserClient{}
	c := &Clients{User: fake}

	for i := 0; i < 3; i++ {
		if err := c.ensureRegistered(context.Background(), testIdentity); err != nil {
			t.Fatalf("call %d: ensureRegistered = %v, want nil", i, err)
		}
	}

	if got := fake.callCount(); got != 3 {
		t.Errorf("Register called %d times across 3 calls, want 3: a client whose "+
			"account was deleted must be able to re-register on its next reconnect", got)
	}
}

// TestARecoveredRegistrationReopensTheStream is the end-to-end statement of
// the property above, through runOnce rather than ensureRegistered alone: a
// server that refuses the stream because the caller's row went away must be
// reconnectable once registration is redone, with no process restart.
func TestARecoveredRegistrationReopensTheStream(t *testing.T) {
	fake := &fakeUserClient{}
	c := &Clients{User: fake}

	// First attempt: registered, but the server has forgotten the account and
	// refuses the stream exactly as ClearanceInterceptor.resolveCaller does.
	// ensureRegistered takes the identity; runOnce's seam does not, so bind it
	// the way RunClientActionStream does.
	ensure := func(ctx context.Context) error { return c.ensureRegistered(ctx, testIdentity) }

	refused := connect.NewError(connect.CodeFailedPrecondition, errors.New("caller is not registered"))
	streams := &scriptedStreams{script: []streamAttempt{
		{recvErr: refused},
		{actions: []*pb.OpenClientActionStreamResp{action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST)}},
	}}
	open := streams.opener()

	outcome, _ := runOnce(context.Background(), open, ensure, testIdentity, ActionHandlers{})
	if outcome != streamRejected {
		t.Fatalf("first attempt outcome = %v, want %v", outcome, streamRejected)
	}

	// Second attempt: ensureRegistered must run AGAIN — that is the whole
	// recovery mechanism — and the stream must then open.
	handled := make(chan struct{}, 1)
	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			handled <- struct{}{}
		},
	}
	if _, err := runOnce(context.Background(), open, ensure, testIdentity, handlers); err == nil {
		t.Fatal("second attempt returned no error; the scripted stream always ends")
	}

	select {
	case <-handled:
	default:
		t.Error("the second attempt delivered no action, so the stream never reopened")
	}

	if got := fake.callCount(); got != 2 {
		t.Errorf("Register called %d times over two attempts, want 2", got)
	}
}

// TestEnsureRegisteredRetriesAfterAFailure: a failure is not "registered", so
// the NEXT call must try again rather than silently treating an unregistered
// client as done — the no-op memoisation above must only latch on success.
func TestEnsureRegisteredRetriesAfterAFailure(t *testing.T) {
	fake := &fakeUserClient{err: connect.NewError(connect.CodeUnavailable, errors.New("no route to server"))}
	c := &Clients{User: fake}

	err := c.ensureRegistered(context.Background(), testIdentity)
	if err == nil {
		t.Fatal("ensureRegistered succeeded, want the scripted failure to propagate")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Errorf("code = %v, want %v", got, connect.CodeUnavailable)
	}

	fake.err = nil
	if err := c.ensureRegistered(context.Background(), testIdentity); err != nil {
		t.Fatalf("second call = %v, want nil once the server accepts", err)
	}

	if got := fake.callCount(); got != 2 {
		t.Errorf("Register called %d times, want 2 (the failed attempt and the retry that succeeded)", got)
	}
}

// TestClientsCloseOnALiteralConstructedValueDoesNotPanic: a *Clients built as
// a literal with only some fields set — the supported construction Options
// documents for injecting fakes — must be safe to Close even though it never
// went through Dial and therefore never built whatever unexported transport
// state a dialled *Clients would carry.
func TestClientsCloseOnALiteralConstructedValueDoesNotPanic(t *testing.T) {
	c := &Clients{}
	c.Close()
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
