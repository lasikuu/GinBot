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
	"google.golang.org/protobuf/proto"
)

func TestMain(m *testing.M) {
	// log.Z stays nil until a binary calls log.InitializeLogger.
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

const actionReminderID = "0192f000-0000-7000-8000-000000000001"

// action carries a populated reminder_delivery arm, so a dispatch passing a
// freshly built message instead of the received one is caught.
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

// actionWithoutPayload leaves the oneof arm unset.
func actionWithoutPayload(t *testing.T, clientAction pb.ClientAction) *pb.OpenClientActionStreamResp {
	t.Helper()

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &clientAction,
	}.Build()
}

// observeLogs installs a recording logger for one test. log.Z is read without
// synchronisation by the package's goroutines, so a test that starts one must
// join it before returning or the reassignment races.
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

// durationFields reads by value, not field name, so the log line can be reworded.
func durationFields(entry observer.LoggedEntry) []time.Duration {
	var durations []time.Duration
	for _, value := range entry.ContextMap() {
		if duration, ok := value.(time.Duration); ok {
			durations = append(durations, duration)
		}
	}

	return durations
}

var testIdentity = StreamIdentity{
	Platform:    pb.Platform_PLATFORM_DISCORD,
	PlatformUID: "caller-uid",
	Username:    "caller",
}

// alwaysEnsure is an `ensure` that always succeeds.
func alwaysEnsure(context.Context) error { return nil }

// waitForCount blocks until count() reports at least want.
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

// fakeActionStream is the in-memory double for actionStream;
// *connect.BidiStreamForClient cannot be constructed without a real server.
type fakeActionStream struct {
	// sendErr breaks the registration message on a stream that did open.
	sendErr error

	mu   sync.Mutex
	sent []*pb.OpenClientActionStreamReq
	// actions are delivered in order, then recvErr ends the stream.
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

	// io.EOF, not (nil, nil): the receive loop only ends on an error, so a
	// missing terminal error would hang the suite instead of failing.
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
// message gets through, and how the stream then ends. There is no "open failed"
// field: OpenClientActionStream has no error return, so streamUnreachable is
// reached only via ensureRegistered or Send failing.
type streamAttempt struct {
	// sendErr breaks the registration message on a stream that did open.
	sendErr error
	// recvErr is the stream's terminal status; unset it is io.EOF.
	recvErr error
	// actions are delivered by Receive, in order, before recvErr ends the stream.
	actions []*pb.OpenClientActionStreamResp
}

// scriptedStreams plays one streamAttempt per connection attempt, repeating the
// last entry once the script runs out so "every attempt fails" is a single entry
// and a runaway loop fails on the caller's deadline, not an index panic.
type scriptedStreams struct {
	script []streamAttempt

	mu       sync.Mutex
	attempts int
	streams  []*fakeActionStream
}

// opener returns an actionStreamOpener backed by this script, with a fresh
// *fakeActionStream per call so consumed actions do not carry across reconnects.
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

// countingEnsure is an `ensure` func that counts calls and either always
// succeeds or always fails with err.
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
	// dispatch must hand the received message through intact, arm and contents.
	if !got.HasReminderDelivery() {
		t.Fatalf("handler received payload arm %v, want reminder_delivery", got.WhichPayload())
	}
	if id := got.GetReminderDelivery().GetReminderId(); id != actionReminderID {
		t.Errorf("handler received reminder id %q, want %q", id, actionReminderID)
	}
}

// An unset oneof arm is well-formed, so dispatch must deliver it like any other.
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
// delay it is parked in.
func TestRunClientActionStreamStopsOnContextCancel(t *testing.T) {
	ensure := &countingEnsure{err: errors.New("registration refused")}
	streams := &scriptedStreams{}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		runClientActionStream(ctx, streams.opener(), ensure.fn(), testIdentity, nil, time.After)
	}()

	// Cancel only after an attempt: cancelling first exits through the loop's
	// top ctx.Err() check, a weaker path.
	waitForCount(t, ensure.callCount, 1)
	cancel()

	// Measured against the delay the loop is sitting in.
	firstDelay := nextBackoff(reconnectMinBackoff, streamUnreachable)

	select {
	case <-done:
	case <-time.After(firstDelay / 2):
		t.Errorf("RunClientActionStream did not return within %v of cancellation; it is parked in a %v delay, so it is waiting that out instead of selecting on ctx.Done()",
			firstDelay/2, firstDelay)

		// Joined even on failure: a leaked goroutine logging through log.Z would
		// race the next test that reassigns it.
		select {
		case <-done:
		case <-time.After(4 * firstDelay):
			t.Fatal("RunClientActionStream never returned at all")
		}
	}

	if got := ensure.callCount(); got != 1 {
		t.Errorf("ensureRegistered was attempted %d times after one cancellation, want 1", got)
	}
	if got := streams.attemptCount(); got != 0 {
		t.Errorf("a stream was opened %d times despite ensureRegistered failing first", got)
	}
}

// recordingClock stands in for time.After inside runClientActionStream: it
// records the delay asked for and returns an already-fired channel, so the
// schedule can be asserted at full speed.
type recordingClock struct {
	// stopAfter bounds the run: the context is cancelled once this many delays
	// have been recorded.
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
// outcomes and returns the delays it waited, in order.
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
		t.Fatalf("runClientActionStream did not return after %d recorded delays; the loop is not bounded by its context", delays)
	}

	return clock.recorded()
}

// TestTheReconnectDelaysTheLoopWaits pins the schedule, exact values and all.
func TestTheReconnectDelaysTheLoopWaits(t *testing.T) {
	// unreachable: the stream opened but the registration Send failed, as a
	// connection that never came up would.
	unreachable := streamAttempt{sendErr: errors.New("connection refused")}
	dropped := streamAttempt{recvErr: connect.NewError(connect.CodeUnavailable, errors.New("transport is closing"))}
	refused := streamAttempt{recvErr: connect.NewError(connect.CodeResourceExhausted, errors.New("too many client action streams"))}

	tests := []struct {
		name   string
		script []streamAttempt
		want   []time.Duration
	}{
		{
			// Starts at 2s, not 1s: nextBackoff is applied before the wait.
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
			// One healthy stream clears the penalty on the NEXT delay, not the
			// one after it.
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
			// A refusal arrives as the terminal status of a stream that opened,
			// so a healthy-drop classification would reset the delay and hammer
			// the registry cap once a second.
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

			// Checked on every delay: a ladder that overshoots then clamps back
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
// retuned to.
func TestARefusedClientsDelaysNeverReturnToTheFloor(t *testing.T) {
	refused := streamAttempt{recvErr: connect.NewError(connect.CodeResourceExhausted, errors.New("too many client action streams"))}

	for i, delay := range driveReconnects(t, []streamAttempt{refused}, 6) {
		if delay <= reconnectMinBackoff {
			t.Errorf("delay %d of a refused client = %v, want it above the %v floor rather than reset to it; a refusal is not a healthy drop",
				i+1, delay, reconnectMinBackoff)
		}
	}
}

// The logged retry_in must equal the delay the loop then waits. The script needs
// a recovery: escalating failures alone log and wait the same value regardless
// of ordering.
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

// saturate drives nextBackoff with one outcome until it stops growing, checking
// the cap is never exceeded at each step. maxSteps only bounds a runaway loop.
func saturate(t *testing.T, outcome streamOutcome) time.Duration {
	t.Helper()

	const maxSteps = 64

	backoff := reconnectMinBackoff
	for i := range maxSteps {
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

// An unreachable server must not be hammered once a second forever.
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

// Only an established stream clears an accumulated backoff.
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

// A ResourceExhausted refusal arrives after the stream was established; read as
// a healthy drop it would reset the delay and retry once a second forever.
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

// The backoff decision hangs on this classification.
func TestRunOnceClassifiesAnEndedStream(t *testing.T) {
	tests := []struct {
		name    string
		recvErr error
		want    streamOutcome
	}{
		{"registry at capacity", connect.NewError(connect.CodeResourceExhausted, errors.New("too many client action streams")), streamRejected},
		{"caller refused", connect.NewError(connect.CodePermissionDenied, errors.New("not a platform client")), streamRejected},
		{"stream not served", connect.NewError(connect.CodeUnimplemented, errors.New("unknown service")), streamRejected},
		{"caller not yet registered", connect.NewError(connect.CodeFailedPrecondition, errors.New("caller is not registered")), streamRejected},
		// A genuine drop must not penalise the next attempt.
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

// ensureRegistered failing establishes nothing, so the attempt is unreachable.
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

// A stream that opened but could not carry the hello Send was never established.
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

// runOnce sends exactly one hello: connect issues the HTTP request lazily on
// the first Send, so without it the handler is never reached.
func TestRunOnceSendsExactlyOneEmptyHelloToOpenTheStream(t *testing.T) {
	streams := &scriptedStreams{script: []streamAttempt{{}}}

	// No recvErr and no actions ends the stream with io.EOF, reported as
	// streamEstablished; only the outcome matters here.
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
		t.Fatalf("%d hello messages were sent, want exactly 1", len(sent))
	}
	if sent[0] == nil {
		t.Fatal("the hello message was nil")
	}
	if !proto.Equal(sent[0], pb.OpenClientActionStreamReq_builder{}.Build()) {
		t.Errorf("hello message = %+v, want the canonical empty OpenClientActionStreamReq", sent[0])
	}
}

// fakeUserClient implements only Register; any other call panics through the
// embedded nil interface.
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

// ensureRegistered must not latch on success: an account deleted underneath a
// long-lived client must be re-registerable on the next reconnect.
func TestEnsureRegisteredRunsOnEveryAttempt(t *testing.T) {
	fake := &fakeUserClient{}
	c := &Clients{User: fake}

	for i := range 3 {
		if err := c.ensureRegistered(context.Background(), testIdentity); err != nil {
			t.Fatalf("call %d: ensureRegistered = %v, want nil", i, err)
		}
	}

	if got := fake.callCount(); got != 3 {
		t.Errorf("Register called %d times across 3 calls, want 3: a client whose "+
			"account was deleted must be able to re-register on its next reconnect", got)
	}
}

// A stream refused because the caller's row went away must reopen once
// registration is redone, with no process restart.
func TestARecoveredRegistrationReopensTheStream(t *testing.T) {
	fake := &fakeUserClient{}
	c := &Clients{User: fake}

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

	// Second attempt: ensureRegistered must run again and the stream must open.
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

// A failed ensureRegistered must be retried on the next call.
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

// A *Clients literal that never went through Dial must be safe to Close.
func TestClientsCloseOnALiteralConstructedValueDoesNotPanic(t *testing.T) {
	c := &Clients{}
	c.Close()
}

// dispatchNoPanic calls dispatch and reports an escaping panic as a test failure.
func dispatchNoPanic(t *testing.T, in *pb.OpenClientActionStreamResp, handlers ActionHandlers) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("a handler panic escaped dispatch: %v", r)
		}
	}()

	dispatch(context.Background(), in, handlers)
}

// A panicking handler must not escape dispatch and kill the client process.
func TestDispatchSurvivesAPanickingHandler(t *testing.T) {
	handlers := ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST: func(context.Context, *pb.OpenClientActionStreamResp) {
			panic("handler blew up")
		},
	}

	dispatchNoPanic(t, action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), handlers)
}

// A handler panic must stay confined to its own delivery.
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
	for range rounds {
		dispatchNoPanic(t, action(t, pb.ClientAction_CLIENT_ACTION_SEND_TEST), handlers)
		dispatchNoPanic(t, action(t, pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION), handlers)
	}

	if notified != rounds {
		t.Errorf("the healthy handler ran %d times across %d rounds, want %d", notified, rounds, rounds)
	}
}

// A recovered panic must be logged with its value, or the lost action is
// undiagnosable.
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

	// The value is what says which handler to look at.
	if recorded := fmt.Sprint(logs.All()); !strings.Contains(recorded, panicValue) {
		t.Errorf("the recovered panic value %q is absent from the log record: %s", panicValue, recorded)
	}
}
