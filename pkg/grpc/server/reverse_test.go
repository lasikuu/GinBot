package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// logs records everything this package logs at warn level or above.
//
// It is installed once, for the whole binary, rather than swapped in by the one
// test that reads it. log.Z is a package global that goroutines started by
// OpenClientActionStream read without synchronisation, so assigning it from a
// test is the write half of a data race against any handler still unwinding —
// see pkg/grpc/client's TestARefusedClientBacksOffRatherThanRetryingEverySecond
// for what that costs. observer.ObservedLogs is mutex-guarded, so concurrent
// writers are fine; a test that wants a clean slate calls TakeAll first.
var logs *observer.ObservedLogs

// logCore is kept so an integration test that has to build a real logger can tee
// the observer back in rather than replacing it. See preserveObservedLogs.
var logCore zapcore.Core

func TestMain(m *testing.M) {
	// The server logs unconditionally; give it a logger rather than a nil
	// pointer. Recording rather than nop, so the warn-level assertions below
	// have something to read; info and debug are still dropped.
	logCore, logs = observer.New(zapcore.WarnLevel)
	log.Z = zap.New(logCore)
	log.S = log.Z.Sugar()
	m.Run()
}

// preserveObservedLogs re-attaches logCore after something has replaced log.Z.
//
// log.InitializeLogger assigns log.Z wholesale, so calling it mid-binary — which
// requireDatabase must, to get readable console output for the integration
// suite — silently discards the observer TestMain installed. Nothing fails at
// the call site; instead every later assertion that reads `logs` sees an empty
// recorder and reports the log line as missing. That cost a real debugging
// detour, so the two are teed rather than one clobbering the other.
func preserveObservedLogs() {
	log.Z = zap.New(zapcore.NewTee(log.Z.Core(), logCore))
	log.S = log.Z.Sugar()
}

// hasDurationField reports whether an entry carries want as one of its fields.
//
// Matched on the value and not on the field name, so the log line stays free to
// be reworded or its fields renamed. What must not change is that the bound the
// handler gave up on is recoverable from the record: an overrun warning that
// does not say how long it waited tells an operator nothing actionable.
func hasDurationField(entry observer.LoggedEntry, want time.Duration) bool {
	for _, value := range entry.ContextMap() {
		if duration, ok := value.(time.Duration); ok && duration == want {
			return true
		}
	}

	return false
}

// clientCount reports how many clients are registered.
func (s *ReverseServer) clientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// clientCountFor reports how many clients are registered for a platform.
func (s *ReverseServer) clientCountFor(platform pb.Platform) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, c := range s.clients {
		if c.platform == platform {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// waitForClientCount is waitFor specialised to the registry, because "the slot
// was released" is the assertion the teardown tests exist to make and waitFor's
// bare "condition not met" does not report it.
func waitForClientCount(t *testing.T, s *ReverseServer, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.clientCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}

	t.Fatalf("clientCount() = %d at the deadline, want %d: the registry slot was never released",
		s.clientCount(), want)
}

// testAction is the development-only heartbeat, used throughout this file as an
// action whose CONTENT is irrelevant: every test below is about routing,
// fan-out, buffering or teardown, none of which reads the payload.
//
// The reminder delivery that does carry a payload worth asserting lives in
// reminder_delivery_test.go.
func testAction(platform pb.Platform) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST
	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: platform.Enum(),
		ClientAction: &action,
		Test:         pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build(),
	}.Build()
}

// ── Driving OpenClientActionStream over a real transport ────────────────────
//
// *connect.BidiStream[Req, Resp] cannot be constructed outside
// connectrpc.com/connect — its only field is an unexported StreamingHandlerConn
// and its only constructor is NewBidiStreamHandler, which is reached solely by
// serving a real HTTP request. That retires the fakeStream this file used to
// drive the handler directly: there is no longer a way to unit-test
// OpenClientActionStream without a real client on the other end of a real
// connection. reverseHarness and openRegisteredReverseClient below are that
// real transport, built on the same harness every other handler test in this
// package uses.
//
// One casualty of this is real: TestUnspecifiedPlatformDoesNotRegister, which
// used to drive the handler's own defensive PLATFORM_UNSPECIFIED check with the
// validation interceptor bypassed, is no longer reachable in isolation — every
// path to the handler now goes through that interceptor. Its coverage is
// reverse_validation_test.go's TestAnUnspecifiedPlatformIsRejectedByTheInterceptor,
// which is what actually reaches production traffic. Likewise the panic-in-Recv
// tests: provoking a panic inside a *connect.BidiStream's own Receive requires a
// genuine bug in connectrpc.com/connect, which cannot be injected from here.
// Neither omission is silent — both are recorded in the port's test report.

const (
	reverseCallerUID    = "reverse-caller"
	reverseCallerUserID = "018f0000-0000-7000-8000-0000000000d0"
)

// reverseHarness registers one identity, valid on both platforms this file
// tests with, at CLEARANCE_REGISTERED — the floor OpenClientActionStream now
// enforces.
func reverseHarness(t *testing.T) *harness {
	t.Helper()

	user := testUser(reverseCallerUserID, pb.Clearance_CLEARANCE_REGISTERED)
	dir := newDirectory().
		add(pb.Platform_PLATFORM_DISCORD, reverseCallerUID, user).
		add(pb.Platform_PLATFORM_MATRIX_PROTOCOL, reverseCallerUID, user)

	return newHarness(t, withDirectory(dir))
}

// streamRecorder is the real-transport equivalent of fakeStream.sent: every
// message a client goroutine actually received, in order, safe to read
// concurrently with the goroutine still appending to it.
type streamRecorder struct {
	mu   sync.Mutex
	recv []*pb.OpenClientActionStreamResp
}

func (r *streamRecorder) record(resp *pb.OpenClientActionStreamResp) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recv = append(r.recv, resp)
}

func (r *streamRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.recv)
}

func (r *streamRecorder) at(i int) *pb.OpenClientActionStreamResp {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recv[i]
}

// reverseStream is a bidi stream to Reverse, plus what came in on it.
//
// Ending it cleanly needs care the fakeStream this replaced did not: cancelling
// the client's own context deregisters the client on the SERVER side promptly
// (Go's http2 transport does propagate cancellation onto the wire), but it does
// NOT reliably unblock the CLIENT's own pending Receive — that needs an
// explicit CloseRequest/CloseResponse, confirmed empirically against this
// exact client stack. A stream a test forgot to close either way leaves its
// underlying HTTP/2 connection pooled-but-active, which is what makes
// httptest.Server.Close hang in cleanup rather than the one test that leaked
// it. openRegisteredReverseClient therefore always registers a t.Cleanup that
// does all three, once, regardless of what the test itself does — it is not
// merely a convenience, it is what keeps a mid-test t.Fatal from leaking a
// connection for the rest of the suite.
type reverseStream struct {
	stream *connect.BidiStreamForClient[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp]
	rec    *streamRecorder
	// done closes when the client's receive loop returns — the real-transport
	// equivalent of requireReturn observing a handler's return value, except
	// this observes the CLIENT side, which is all that is left to observe
	// once the handler's own return value is inside the framework.
	done <-chan struct{}
	// cancel ends the client's OWN request context, the real-transport
	// equivalent of a platform client vanishing without a clean goodbye. It
	// deregisters the client on the SERVER side quickly; it does NOT reliably
	// unblock this client's own pending Receive (see the type comment), so a
	// test asserting the effect of cancel ALONE must check server-side state
	// (clientCount), not c.done.
	cancel context.CancelFunc
}

// drainReverseStream starts a goroutine that reads every message until the
// stream ends, recording each one. The returned channel closes when reading
// stops, which is the client-side signal that the server ended the stream —
// by Shutdown, by refusal, or by the connection itself going away.
func drainReverseStream(stream *connect.BidiStreamForClient[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp], rec *streamRecorder) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			resp, err := stream.Receive()
			if err != nil {
				return
			}
			rec.record(resp)
		}
	}()
	return done
}

// openRegisteredReverseClient opens a real bidi stream as a registered
// caller, sends the registration message, and waits until the server has
// actually admitted it — so a later assertion cannot pass by racing ahead of
// the registration the way a bare Send does not guarantee.
func openRegisteredReverseClient(t *testing.T, h *harness, platform pb.Platform) *reverseStream {
	t.Helper()

	ctx, cancel := context.WithCancel(callerCtx(platform, reverseCallerUID))
	stream := h.Reverse.OpenClientActionStream(ctx)

	before := h.reverseServer.clientCount()
	if err := stream.Send(pb.OpenClientActionStreamReq_builder{PlatformEnum: platform.Enum()}.Build()); err != nil {
		cancel()
		t.Fatalf("send registration: %v", err)
	}

	// Registered before the wait below, not after: if registration itself
	// times out and fails the test, a t.Cleanup registered only by the
	// caller — after this function returns — would never run at all, because
	// t.Fatal unwinds this goroutine immediately. Registering here first
	// guarantees the stream is always torn down, however this function exits.
	t.Cleanup(sync.OnceFunc(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		cancel()
	}))

	rec := &streamRecorder{}
	done := drainReverseStream(stream, rec)

	waitFor(t, func() bool { return h.reverseServer.clientCount() == before+1 })

	return &reverseStream{stream: stream, rec: rec, done: done, cancel: cancel}
}

// requireStreamEnded waits for a client's receive loop to stop and fails the
// test instead of hanging the suite when it does not.
func requireStreamEnded(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not end within the deadline", what)
	}
}

// ── A sender stuck inside Send (see the test report) ────────────────────────
//
// TestAStuckSenderDoesNotPinTheHandler is NOT ported. Forcing the sender
// goroutine's stream.Send to genuinely block requires exhausting the real
// transport's HTTP/2 flow-control window — the old fakeStream simulated this
// with a channel a test could hold closed on demand, and there is no
// equivalent seam left: *connect.BidiStream has no exported constructor
// outside connectrpc.com/connect (see the type comment on reverseStream and
// on drainReverseStream), so the ONLY way to reach the handler at all is a
// real connection, and a real connection's flow-control window size is not a
// fast, deterministic knob this test author has reliable access to. What IS
// still testable without a transport is that the field the drain-timeout
// logic reads is wired to the constant reverse.go documents.
func TestNewReverseServerDefaultsToTheDocumentedDrainTimeout(t *testing.T) {
	s := NewReverseServer()
	if s.senderDrainTimeout != defaultSenderDrainTimeout {
		t.Errorf("senderDrainTimeout = %v, want the documented default %v", s.senderDrainTimeout, defaultSenderDrainTimeout)
	}
}

// ── Fan-out and routing ──────────────────────────────────────────────────────

// The original implementation drained a single shared channel, so an action
// reached exactly one arbitrary stream. Every client on the platform must get it.
func TestSendActionFansOutToAllClientsOnPlatform(t *testing.T) {
	h := reverseHarness(t)

	clients := make([]*reverseStream, 3)
	for i := range clients {
		clients[i] = openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)
	}

	h.reverseServer.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	for _, c := range clients {
		waitFor(t, func() bool { return c.rec.count() >= 1 })
	}

	// Settle, then assert exactly one — waitFor alone cannot distinguish
	// "one" from "one so far", so duplicate delivery would slip past it.
	time.Sleep(50 * time.Millisecond)
	for i, c := range clients {
		if got := c.rec.count(); got != 1 {
			t.Errorf("client %d received %d actions, want exactly 1", i, got)
		}
	}
}

// platformClients was written but never read, so routing did not exist.
func TestSendActionRoutesByPlatform(t *testing.T) {
	h := reverseHarness(t)

	discord := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)
	matrix := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_MATRIX_PROTOCOL)

	h.reverseServer.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	waitFor(t, func() bool { return discord.rec.count() == 1 })

	time.Sleep(50 * time.Millisecond)
	if got := matrix.rec.count(); got != 0 {
		t.Errorf("matrix client received %d actions, want 0", got)
	}
}

// The sender goroutine used to outlive the stream, leaking one per connection.
func TestClientIsDeregisteredOnDisconnect(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	if err := c.stream.CloseRequest(); err != nil {
		t.Fatalf("CloseRequest: %v", err)
	}

	requireStreamEnded(t, c.done, "the client's receive loop after CloseRequest")
	waitForClientCount(t, h.reverseServer, 0)
}

// TestClientIsDeregisteredAfterSuccessfulTrafficThenDisconnect is the
// real-transport replacement for the old fakeStream "Send fails" tests: a
// broken Send can no longer be forced directly, since nothing outside
// connectrpc.com/connect can inject a transport failure into a real stream.
// What is preserved is the property those tests actually protected — a
// client that received real traffic and then disconnects must still be
// deregistered, not left holding a slot and a full send buffer forever.
func TestClientIsDeregisteredAfterSuccessfulTrafficThenDisconnect(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	h.reverseServer.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))
	waitFor(t, func() bool { return c.rec.count() >= 1 })

	if got := h.reverseServer.clientCount(); got != 1 {
		t.Fatalf("clientCount() = %d after a successful send, want the client still registered", got)
	}

	if err := c.stream.CloseRequest(); err != nil {
		t.Fatalf("CloseRequest: %v", err)
	}

	waitForClientCount(t, h.reverseServer, 0)
}

// A client that vanishes without closing cleanly cancels the stream context.
// That has to unwind the handler and release its slot on its own, with no
// shutdown involved.
// Asserted on the SERVER's registry, not on c.done: cancelling the client's
// own context propagates onto the wire and deregisters the client on the
// server side promptly, but it does not reliably unblock this client's own
// pending Receive call — confirmed empirically against this exact client
// stack — so c.done is not a safe signal for this specific teardown path.
// See the comment on reverseStream for the full explanation and why
// openRegisteredReverseClient's own cleanup does not rely on cancel alone
// either.
func TestClientContextCancellationDeregistersTheClient(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	c.cancel()

	waitForClientCount(t, h.reverseServer, 0)
}

// SendAction must never block, even with no consumer draining the buffer.
// Registered directly rather than through a stream, so nothing drains it —
// there is no client-side equivalent for "a sender that never reads at all"
// over a real bidi stream without the transport itself intervening first.
func TestSendActionDoesNotBlockWhenBufferIsFull(t *testing.T) {
	s := NewReverseServer()

	client, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)
	if !ok {
		t.Fatal("register refused the first client on an empty registry")
	}
	defer deregister()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < clientSendBuffer*3; i++ {
			s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SendAction blocked on a full client buffer")
	}

	if got := len(client.actions); got != clientSendBuffer {
		t.Errorf("queued %d actions, want the buffer cap %d", got, clientSendBuffer)
	}
}

func TestSendActionWithNoClientsIsANoop(t *testing.T) {
	s := NewReverseServer()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SendAction blocked with no clients connected")
	}
}

// ── The registry cap ─────────────────────────────────────────────────────────
//
// Without a cap the registry is an unbounded map keyed on nothing an operator
// controls: OpenClientActionStream is a STREAM, and every registered caller —
// including one deliberately opening streams in a loop — could otherwise grow
// it without limit. Each admitted client costs a clientSendBuffer-deep channel
// of *pb.OpenClientActionStreamResp plus a goroutine, and SendAction walks the
// whole map under RLock on every action — so an unbounded registry is both a
// memory and a latency amplifier. These tests exercise register/deregister
// directly: the cap itself is registry-level bookkeeping with no transport
// dependency, so it needs no stream at all.

// fillRegistry registers exactly maxStreamClients clients and schedules their
// removal. Every registration is asserted, so a cap that is lower than
// maxStreamClients fails here rather than surfacing as a confusing count
// mismatch later.
func fillRegistry(t *testing.T, s *ReverseServer) {
	t.Helper()

	for i := 0; i < maxStreamClients; i++ {
		_, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)
		if !ok {
			t.Fatalf("register refused client %d of %d, before the cap was reached", i+1, maxStreamClients)
		}
		t.Cleanup(deregister)
	}
}

// TestRegisterAdmitsExactlyMaxStreamClients: the cap is inclusive — the
// maxStreamClients'th client is admitted, the next one is not.
func TestRegisterAdmitsExactlyMaxStreamClients(t *testing.T) {
	s := NewReverseServer()
	fillRegistry(t, s)

	if got := s.clientCount(); got != maxStreamClients {
		t.Errorf("clientCount() = %d after %d successful registrations, want %d",
			got, maxStreamClients, maxStreamClients)
	}
}

// TestRegisterRefusesPastTheCapWithoutGrowingTheRegistry.
//
// The nil returns are asserted rather than ignored: a refused registration
// that still handed back a deregister closure would, when the caller's defer
// ran, delete an id from the map and close a channel it never owned — turning
// a refusal into a panic on another client's send.
func TestRegisterRefusesPastTheCapWithoutGrowingTheRegistry(t *testing.T) {
	s := NewReverseServer()
	fillRegistry(t, s)

	client, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)

	if ok {
		// Admitted when it should not have been. Release it so the rest of the
		// test still sees an accurate count, and stop: the nil checks below are
		// about the REFUSAL contract and would otherwise report a contradictory
		// second failure for a client that was legitimately returned.
		t.Error("register admitted a client past maxStreamClients")
		if deregister != nil {
			deregister()
		}
		return
	}
	if client != nil {
		t.Errorf("register returned a non-nil client (%+v) alongside ok == false", client)
	}
	if deregister != nil {
		t.Error("register returned a non-nil deregister alongside ok == false")
	}
	if got := s.clientCount(); got != maxStreamClients {
		t.Errorf("clientCount() = %d after a refused registration, want it unchanged at %d", got, maxStreamClients)
	}
}

// TestRegisterRefusesRegardlessOfPlatform: the cap bounds the whole registry,
// not one platform's share of it. SendAction walks every entry under RLock
// irrespective of platform, so a second platform is not free capacity.
func TestRegisterRefusesRegardlessOfPlatform(t *testing.T) {
	s := NewReverseServer()
	fillRegistry(t, s)

	if _, _, ok := s.register(pb.Platform_PLATFORM_MATRIX_PROTOCOL); ok {
		t.Error("register admitted a Matrix client past a cap already filled by Discord clients")
	}
	if got := s.clientCountFor(pb.Platform_PLATFORM_MATRIX_PROTOCOL); got != 0 {
		t.Errorf("clientCountFor(MATRIX) = %d, want 0", got)
	}
}

// TestOpenClientActionStreamPastTheCapIsResourceExhausted: the refusal has to
// reach the client as a code it can act on. ResourceExhausted specifically,
// because it tells a reconnecting platform client to back off rather than to
// re-register (FailedPrecondition) or to give up on a malformed request
// (InvalidArgument). Driven over a real client, since the code has to survive
// the whole Connect error-marshalling round trip, not just the handler's
// return value.
func TestOpenClientActionStreamPastTheCapIsResourceExhausted(t *testing.T) {
	h := reverseHarness(t)
	fillRegistry(t, h.reverseServer)

	ctx, cancel := context.WithCancel(callerCtx(pb.Platform_PLATFORM_DISCORD, reverseCallerUID))
	defer cancel()
	stream := h.Reverse.OpenClientActionStream(ctx)

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()); err != nil {
		t.Fatalf("send registration: %v", err)
	}

	_, err := stream.Receive()
	if err == nil {
		t.Fatal("the stream was admitted past the registry cap")
	}
	requireCode(t, err, codes.ResourceExhausted)

	if got := h.reverseServer.clientCount(); got != maxStreamClients {
		t.Errorf("clientCount() = %d after a refused stream, want it unchanged at %d", got, maxStreamClients)
	}
}

// TestACapacityFreedByADeregistrationIsReusable: the cap must be a live
// occupancy check, not a monotonic counter. A cap implemented against nextID
// would pass every test above and then permanently refuse every client after
// the 64th connection in the process's lifetime — a bot that stops working
// after a day of ordinary reconnects.
func TestACapacityFreedByADeregistrationIsReusable(t *testing.T) {
	s := NewReverseServer()

	// Registered manually rather than through fillRegistry, because this test
	// needs to release one of them mid-test rather than at cleanup.
	deregisters := make([]func(), 0, maxStreamClients)
	for i := 0; i < maxStreamClients; i++ {
		_, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)
		if !ok {
			t.Fatalf("register refused client %d of %d", i+1, maxStreamClients)
		}
		deregisters = append(deregisters, deregister)
	}
	defer func() {
		// The first was already released below; the rest are released here.
		for _, deregister := range deregisters[1:] {
			deregister()
		}
	}()

	if _, _, ok := s.register(pb.Platform_PLATFORM_DISCORD); ok {
		t.Fatal("precondition failed: the registry was not actually full")
	}

	deregisters[0]()

	if got := s.clientCount(); got != maxStreamClients-1 {
		t.Fatalf("clientCount() = %d after one deregistration, want %d", got, maxStreamClients-1)
	}

	_, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)
	if !ok {
		t.Fatal("register refused a client after capacity was freed by a deregistration")
	}
	defer deregister()

	if got := s.clientCount(); got != maxStreamClients {
		t.Errorf("clientCount() = %d, want %d", got, maxStreamClients)
	}
}

// TestConcurrentRegistrationNeverExceedsTheCap is the assertion the cap
// actually needs, and the reason register returns ok rather than the caller
// checking a count first.
//
// Every real client arrives on its own goroutine, so a check-then-insert
// outside the mutex lets N concurrent callers all observe the registry as
// having room and all insert. Twice the cap is attempted at once and released
// together to maximise contention; exactly maxStreamClients must win. Run
// under -race.
func TestConcurrentRegistrationNeverExceedsTheCap(t *testing.T) {
	s := NewReverseServer()

	const attempts = maxStreamClients * 2

	var (
		admitted  atomic.Int64
		badReturn atomic.Int64
		mu        sync.Mutex
		releases  []func()
		start     sync.WaitGroup
		done      sync.WaitGroup
	)

	start.Add(1)
	for i := 0; i < attempts; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait() // release all goroutines together

			client, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)
			if !ok {
				// A refusal must hand back nothing at all; a non-nil closure
				// here would be double-freeing another client's channel.
				if client != nil || deregister != nil {
					badReturn.Add(1)
				}
				return
			}

			admitted.Add(1)
			mu.Lock()
			releases = append(releases, deregister)
			mu.Unlock()
		}()
	}
	start.Done()
	done.Wait()

	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	if got := admitted.Load(); got != maxStreamClients {
		t.Errorf("%d of %d concurrent registrations were admitted, want exactly the cap %d",
			got, attempts, maxStreamClients)
	}
	if got := badReturn.Load(); got != 0 {
		t.Errorf("%d refused registrations returned a non-nil client or deregister", got)
	}
	if got := s.clientCount(); got != maxStreamClients {
		t.Errorf("clientCount() = %d after %d concurrent registrations, want %d",
			got, attempts, maxStreamClients)
	}
}

// ── Shutdown ─────────────────────────────────────────────────────────────────
//
// OpenClientActionStream used to block unconditionally on stream.Recv(), so a
// registered client with nothing to say held the handler open indefinitely — and
// with it graceful shutdown, which waits for every handler to return. These
// tests are what make a graceful shutdown safe.

// The steady state of a healthy control channel is silence: the client sends its
// registration and then nothing. A shutdown that needs a client message to make
// progress therefore never completes in practice.
func TestShutdownUnblocksASilentRegisteredStream(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	h.reverseServer.Shutdown()

	requireStreamEnded(t, c.done, "OpenClientActionStream after Shutdown")

	// The deregistration lives in a defer, and the new control flow must not
	// step around it — a slot leaked on shutdown is a slot leaked on every
	// restart, and maxStreamClients is finite.
	waitForClientCount(t, h.reverseServer, 0)
}

// A client's platform is unknown until its first message, so a stream can sit in
// its first Receive having registered nothing. That iteration runs before there
// is anything to deregister and must unwind on its own terms.
func TestShutdownUnblocksAStreamThatNeverRegistered(t *testing.T) {
	h := reverseHarness(t)

	ctx, cancel := context.WithCancel(callerCtx(pb.Platform_PLATFORM_DISCORD, reverseCallerUID))
	t.Cleanup(cancel)
	stream := h.Reverse.OpenClientActionStream(ctx)

	// Send(nil) dispatches the request headers without a body message — a
	// Connect client's first Send is what actually puts the request on the
	// wire at all, so without this the handler never starts and Shutdown
	// would trivially "unblock" a request the server never received.
	if err := stream.Send(nil); err != nil {
		t.Fatalf("send headers-only: %v", err)
	}

	rec := &streamRecorder{}
	done := drainReverseStream(stream, rec)

	h.reverseServer.Shutdown()

	requireStreamEnded(t, done, "an unregistered OpenClientActionStream after Shutdown")
	if got := h.reverseServer.clientCount(); got != 0 {
		t.Errorf("clientCount() = %d, want 0: the stream never registered", got)
	}
}

// Shutdown is reachable from a signal handler, from a boot that fails partway,
// and from test cleanup, so a second call is realistic rather than
// hypothetical — and closing an already-closed channel panics, which would turn
// an orderly shutdown into a crash on the way out.
func TestShutdownIsIdempotent(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	h.reverseServer.Shutdown()
	h.reverseServer.Shutdown()
	h.reverseServer.Shutdown()

	requireStreamEnded(t, c.done, "OpenClientActionStream after repeated Shutdown")
}

// The server may be shut down before any platform client has ever connected —
// a failure on the line after Serve, for instance. Nothing to unblock must not
// mean nothing works afterwards, so the ordinary fan-out path is poked too.
func TestShutdownWithNoStreamsIsSafe(t *testing.T) {
	s := NewReverseServer()

	s.Shutdown()
	s.Shutdown()

	s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))
}

// One stream ending is not the contract: shutdown waits for the last one, so a
// shutdown that only unblocks whichever stream it reaches first still hangs
// process teardown. Two platforms are used because routing is per-platform
// and a shutdown must not be.
func TestShutdownTerminatesEveryOpenStream(t *testing.T) {
	h := reverseHarness(t)

	platforms := []pb.Platform{
		pb.Platform_PLATFORM_DISCORD,
		pb.Platform_PLATFORM_DISCORD,
		pb.Platform_PLATFORM_MATRIX_PROTOCOL,
	}

	clients := make([]*reverseStream, 0, len(platforms))
	for _, platform := range platforms {
		c := openRegisteredReverseClient(t, h, platform)
		clients = append(clients, c)
	}

	h.reverseServer.Shutdown()

	for _, c := range clients {
		requireStreamEnded(t, c.done, "OpenClientActionStream after Shutdown")
	}

	waitForClientCount(t, h.reverseServer, 0)
}
