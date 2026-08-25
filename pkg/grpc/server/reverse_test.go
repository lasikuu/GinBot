package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"
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

// fakeStream implements the server side of the bidi stream in memory.
//
// ctx, sendErr, sendFailsAfter and sendBlock are configured before the handler
// starts and never touched again, which mirrors grpc's own ServerStream — its
// context is fixed for the life of the stream — and is why only sent needs the
// mutex.
type fakeStream struct {
	grpc.ServerStream

	recv          chan *pb.OpenClientActionStreamReq
	closeRecvOnce sync.Once

	// recvEntries and recvExits make OpenClientActionStream's receive goroutine
	// observable. It parks in Recv, and nothing in grpc-go cancels a server-side
	// Recv, so a goroutine abandoned there survives its handler and stays alive
	// for the rest of the test binary — invisible to every other assertion here,
	// and the leak class behind the -race failures at -count=5.
	recvEntries atomic.Int64
	recvExits   atomic.Int64

	// panicRecv carries at most one panic value into Recv. A single token rather
	// than a closed channel on purpose: a receive goroutine that recovers and
	// loops would spin on a Recv that panics every time, burning CPU instead of
	// failing anything.
	panicRecv chan any

	// ctx is a field rather than a hardcoded context.Background() so a test can
	// cancel the stream the way a client that vanished does.
	ctx context.Context

	// sendErr is what Send returns once sendFailsAfter sends have succeeded.
	sendErr        error
	sendFailsAfter int

	// sendBlock parks Send until it is closed. sendEntries counts entries to
	// Send, so a test can wait until the sender is genuinely inside it.
	sendBlock   chan struct{}
	releaseOnce sync.Once
	sendEntries atomic.Int64

	mu   sync.Mutex
	sent []*pb.OpenClientActionStreamResp
}

// newFakeStream builds a stream and schedules its release.
//
// The cleanup is not optional bookkeeping. Closing recv is the only thing that
// gets the handler's receive goroutine out of Recv, so a stream left open at the
// end of a test leaves one goroutine parked for the life of the binary; enough
// of those and the suite fails under -race for reasons that have nothing to do
// with the test that failed.
func newFakeStream(t *testing.T) *fakeStream {
	t.Helper()

	f := &fakeStream{
		recv:      make(chan *pb.OpenClientActionStreamReq, 4),
		panicRecv: make(chan any, 1),
		ctx:       context.Background(),
	}
	t.Cleanup(f.closeRecv)

	return f
}

// closeRecv ends the stream from the client's side. Guarded by a Once because
// both a test and newFakeStream's cleanup may reach it, and closing a closed
// channel panics.
func (f *fakeStream) closeRecv() {
	f.closeRecvOnce.Do(func() { close(f.recv) })
}

// withContext replaces the context the handler observes. Call it before the
// handler starts.
func (f *fakeStream) withContext(ctx context.Context) *fakeStream {
	f.ctx = ctx
	return f
}

// failSendsAfter makes the (n+1)'th and every later Send return err. n == 0
// breaks the very first send; a larger n exercises a client that broke
// mid-stream, which a test cannot otherwise distinguish from one that never
// registered successfully in the first place. Call it before the handler starts.
func (f *fakeStream) failSendsAfter(n int, err error) *fakeStream {
	f.sendFailsAfter = n
	f.sendErr = err
	return f
}

// blockSends parks every Send until releaseSends is called. Call it before the
// handler starts.
//
// This is the one state the sender drain timeout exists for: a peer that has
// stopped reading, with a full HTTP/2 flow-control window, leaves the sender
// blocked INSIDE Send rather than at the range statement — so closing the
// client's action channel does not release it, and without a bound the handler
// never returns at all.
func (f *fakeStream) blockSends() *fakeStream {
	f.sendBlock = make(chan struct{})
	return f
}

// releaseSends unparks Send. Safe to call twice, so a test can schedule it as
// cleanup and still release early.
func (f *fakeStream) releaseSends() {
	f.releaseOnce.Do(func() {
		if f.sendBlock != nil {
			close(f.sendBlock)
		}
	})
}

// panicNextRecv arms exactly one panic inside Recv, releasing a call already
// parked there.
//
// Armed at runtime rather than configured up front so a test can register a
// client first and then panic, which is the only way to observe deterministically
// that the registry slot is released: configured up front, registration and
// deregistration happen within microseconds of each other and no poll can
// reliably see the slot occupied in between.
func (f *fakeStream) panicNextRecv(value any) {
	f.panicRecv <- value
}

func (f *fakeStream) Send(resp *pb.OpenClientActionStreamResp) error {
	f.sendEntries.Add(1)

	// Parked holding no lock, so sentCount and the assertions built on it stay
	// answerable while the sender is stuck — and so this models a real Send,
	// which blocks on the transport rather than on anything this fake owns.
	if f.sendBlock != nil {
		<-f.sendBlock
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.sendErr != nil && len(f.sent) >= f.sendFailsAfter {
		return f.sendErr
	}

	f.sent = append(f.sent, resp)
	return nil
}

func (f *fakeStream) Recv() (*pb.OpenClientActionStreamReq, error) {
	f.recvEntries.Add(1)
	defer f.recvExits.Add(1)

	select {
	case value := <-f.panicRecv:
		panic(value)
	case req, ok := <-f.recv:
		if !ok {
			return nil, io.EOF
		}
		return req, nil
	}
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func (f *fakeStream) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// recvParked reports how many Recv calls are in flight, which for this handler
// is 0 or 1: the receive goroutine is either parked in Recv or has left it.
func (f *fakeStream) recvParked() int64 {
	return f.recvEntries.Load() - f.recvExits.Load()
}

// sendEntered reports whether the sender goroutine has reached Send at all,
// which is not the same as SendAction having queued the action for it.
func (f *fakeStream) sendEntered() bool {
	return f.sendEntries.Load() > 0
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

// openStream runs the handler on its own goroutine and hands back its single
// return value, so a test can assert the handler RETURNED rather than that some
// goroutine was merely signalled.
func openStream(s *ReverseServer, stream *fakeStream) <-chan error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.OpenClientActionStream(stream) }()

	return errCh
}

// registerStream opens a stream and waits until the server has admitted it, so
// a later assertion cannot pass by racing ahead of the registration.
func registerStream(t *testing.T, s *ReverseServer, stream *fakeStream, platform pb.Platform) <-chan error {
	t.Helper()

	before := s.clientCount()
	errCh := openStream(s, stream)
	stream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: platform.Enum(),
	}.Build()
	waitFor(t, func() bool { return s.clientCount() == before+1 })

	return errCh
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

// requireReturn waits for a handler to return and fails the test instead of
// hanging the suite when it does not. Every assertion in this file about
// unwinding a stream goes through here: "the handler returned" is the claim, and
// a test that hangs makes no claim at all.
func requireReturn(t *testing.T, errCh <-chan error, what string) error {
	t.Helper()

	select {
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return within the deadline", what)
		return nil
	}
}

// requireRecvGoroutineReleased asserts that OpenClientActionStream's receive
// goroutine is where the handler's teardown assumes it is — parked in Recv — and
// that ending the stream gets it out again.
//
// Call this only after the handler has returned. Parked-THEN-released is the
// claim: a goroutine that had already finished would satisfy the second half for
// the wrong reason, which is why the first half is asserted at all.
//
// What it deliberately does not claim is that the goroutine has terminated.
// Nothing in grpc-go cancels a server-side Recv, so a handler cannot release its
// receive goroutine on its own — handlerDone only frees it from a publish nobody
// will read — and the goroutine necessarily outlives the handler until the
// transport tears the stream down. Proving termination from here would mean
// counting goroutines or matching function names in a stack dump, neither of
// which survives the move to a different RPC framework. What keeps the leak
// bounded instead is newFakeStream scheduling closeRecv for every stream in this
// package, so no test ends with one still parked; this asserts that the release
// actually works.
func requireRecvGoroutineReleased(t *testing.T, stream *fakeStream) {
	t.Helper()

	waitFor(t, func() bool { return stream.recvParked() == 1 })

	stream.closeRecv()

	// Terminating rather than transient: once recv is closed every Recv returns
	// io.EOF immediately, and the goroutine returns on seeing an error, so it
	// cannot re-enter.
	waitFor(t, func() bool { return stream.recvParked() == 0 })
}

func testAction(platform pb.Platform) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST
	content, _ := structpb.NewStruct(map[string]any{"k": "v"})
	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: platform.Enum(),
		ClientAction: &action,
		Content:      content,
	}.Build()
}

// The original implementation drained a single shared channel, so an action
// reached exactly one arbitrary stream. Every client on the platform must get it.
func TestSendActionFansOutToAllClientsOnPlatform(t *testing.T) {
	s := NewReverseServer()

	streams := []*fakeStream{newFakeStream(t), newFakeStream(t), newFakeStream(t)}
	var wg sync.WaitGroup
	for _, st := range streams {
		wg.Add(1)
		go func(st *fakeStream) {
			defer wg.Done()
			_ = s.OpenClientActionStream(st)
		}(st)
	}

	for _, st := range streams {
		st.recv <- pb.OpenClientActionStreamReq_builder{
			PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		}.Build()
	}
	waitFor(t, func() bool { return s.clientCount() == len(streams) })

	s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	for _, st := range streams {
		waitFor(t, func() bool { return st.sentCount() >= 1 })
	}

	// Settle, then assert exactly one — waitFor alone cannot distinguish
	// "one" from "one so far", so duplicate delivery would slip past it.
	time.Sleep(50 * time.Millisecond)
	for i, st := range streams {
		if got := st.sentCount(); got != 1 {
			t.Errorf("stream %d received %d actions, want exactly 1", i, got)
		}
	}

	for _, st := range streams {
		st.closeRecv()
	}
	wg.Wait()
}

// platformClients was written but never read, so routing did not exist.
func TestSendActionRoutesByPlatform(t *testing.T) {
	s := NewReverseServer()

	discordStream := newFakeStream(t)
	matrixStream := newFakeStream(t)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s.OpenClientActionStream(discordStream) }()
	go func() { defer wg.Done(); _ = s.OpenClientActionStream(matrixStream) }()

	discordStream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()
	matrixStream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_MATRIX_PROTOCOL.Enum(),
	}.Build()
	waitFor(t, func() bool { return s.clientCount() == 2 })

	s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	waitFor(t, func() bool { return discordStream.sentCount() == 1 })
	if got := matrixStream.sentCount(); got != 0 {
		t.Errorf("matrix stream received %d actions, want 0", got)
	}

	discordStream.closeRecv()
	matrixStream.closeRecv()
	wg.Wait()
}

// The sender goroutine used to outlive the stream, leaking one per connection.
func TestClientIsDeregisteredOnDisconnect(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.OpenClientActionStream(stream)
	}()

	stream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()
	waitFor(t, func() bool { return s.clientCount() == 1 })

	stream.closeRecv()
	<-done

	if got := s.clientCount(); got != 0 {
		t.Errorf("client count after disconnect = %d, want 0", got)
	}
}

// SendAction must never block, even with no consumer draining the buffer.
func TestSendActionDoesNotBlockWhenBufferIsFull(t *testing.T) {
	s := NewReverseServer()

	// Register a client directly, with no sender goroutine, so nothing drains it.
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

func TestUnspecifiedPlatformDoesNotRegister(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.OpenClientActionStream(stream)
	}()

	stream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_UNSPECIFIED.Enum(),
	}.Build()

	// Then a valid one, whose registration we can wait on deterministically.
	stream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()
	waitFor(t, func() bool { return s.clientCount() == 1 })

	if got := s.clientCountFor(pb.Platform_PLATFORM_UNSPECIFIED); got != 0 {
		t.Errorf("registered %d unspecified-platform clients, want 0", got)
	}

	stream.closeRecv()
	<-done
}

// ── The registry cap ─────────────────────────────────────────────────────────
//
// Without a cap the registry is an unbounded map keyed on nothing an operator
// controls: OpenClientActionStream is a STREAM, so it never passes through the
// unary clearance interceptor, and anything that can reach the gRPC port can
// open one. Each admitted client costs a clientSendBuffer-deep channel of
// *pb.OpenClientActionStreamResp plus a goroutine, and SendAction walks the
// whole map under RLock on every action — so an unbounded registry is both a
// memory and a latency amplifier.

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
// (InvalidArgument).
func TestOpenClientActionStreamPastTheCapIsResourceExhausted(t *testing.T) {
	s := NewReverseServer()
	fillRegistry(t, s)

	stream := newFakeStream(t)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.OpenClientActionStream(stream)
	}()

	stream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()

	select {
	case err := <-errCh:
		requireCode(t, err, codes.ResourceExhausted)
	case <-time.After(2 * time.Second):
		t.Fatal("OpenClientActionStream neither registered nor refused within the deadline")
	}

	if got := s.clientCount(); got != maxStreamClients {
		t.Errorf("clientCount() = %d after a refused stream, want it unchanged at %d", got, maxStreamClients)
	}
	stream.closeRecv()
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
// with it grpcServer.GracefulStop, which waits for every handler to return.
// harness_test.go calls Stop() rather than GracefulStop() specifically to dodge
// that hang, which is the record of the defect being observed and never carried
// back to the production wiring. These tests are what make a graceful shutdown
// safe there.

// The steady state of a healthy control channel is silence: the client sends its
// registration and then nothing. A shutdown that needs a client message to make
// progress therefore never completes in practice.
func TestShutdownUnblocksASilentRegisteredStream(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t)
	errCh := registerStream(t, s, stream, pb.Platform_PLATFORM_DISCORD)

	s.Shutdown()

	if err := requireReturn(t, errCh, "OpenClientActionStream after Shutdown"); err != nil {
		t.Errorf("handler returned %v, want nil: an orderly shutdown is not a stream failure", err)
	}

	// The deregistration lives in a defer, and the new control flow must not
	// step around it — a slot leaked on shutdown is a slot leaked on every
	// restart, and maxStreamClients is finite.
	if got := s.clientCount(); got != 0 {
		t.Errorf("clientCount() = %d after the handler returned, want 0", got)
	}

	// A returned handler is only half of a clean shutdown. This is the canonical
	// shutdown path, so it is where the other half is asserted: the receive
	// goroutine the handler started is parked in Recv, and must be released by
	// the stream ending rather than left alive for the rest of the process.
	requireRecvGoroutineReleased(t, stream)
}

// A client's platform is unknown until its first message, so a stream can sit in
// its first Recv having registered nothing. That iteration runs before there is
// anything to deregister and must unwind on its own terms.
func TestShutdownUnblocksAStreamThatNeverRegistered(t *testing.T) {
	s := NewReverseServer()
	errCh := openStream(s, newFakeStream(t))

	s.Shutdown()

	if err := requireReturn(t, errCh, "an unregistered OpenClientActionStream after Shutdown"); err != nil {
		t.Errorf("handler returned %v, want nil", err)
	}
	if got := s.clientCount(); got != 0 {
		t.Errorf("clientCount() = %d, want 0: the stream never registered", got)
	}
}

// Shutdown is reachable from a signal handler, from a boot that fails partway,
// and from test cleanup, so a second call is realistic rather than
// hypothetical — and closing an already-closed channel panics, which would turn
// an orderly shutdown into a crash on the way out.
func TestShutdownIsIdempotent(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t)
	errCh := registerStream(t, s, stream, pb.Platform_PLATFORM_DISCORD)

	s.Shutdown()
	s.Shutdown()
	s.Shutdown()

	if err := requireReturn(t, errCh, "OpenClientActionStream after repeated Shutdown"); err != nil {
		t.Errorf("handler returned %v, want nil", err)
	}
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

// One stream returning is not the contract: GracefulStop waits for the last one,
// so a shutdown that signals whichever stream it reaches first still hangs the
// process. Two platforms are used because routing is per-platform and a
// shutdown must not be.
func TestShutdownTerminatesEveryOpenStream(t *testing.T) {
	s := NewReverseServer()

	platforms := []pb.Platform{
		pb.Platform_PLATFORM_DISCORD,
		pb.Platform_PLATFORM_DISCORD,
		pb.Platform_PLATFORM_MATRIX_PROTOCOL,
	}

	handlers := make([]<-chan error, 0, len(platforms))
	for _, platform := range platforms {
		handlers = append(handlers, registerStream(t, s, newFakeStream(t), platform))
	}

	s.Shutdown()

	for i, errCh := range handlers {
		err := requireReturn(t, errCh, fmt.Sprintf("OpenClientActionStream %d after Shutdown", i))
		if err != nil {
			t.Errorf("handler %d returned %v, want nil", i, err)
		}
	}

	if got := s.clientCount(); got != 0 {
		t.Errorf("clientCount() = %d after every handler returned, want 0", got)
	}
}

// A client that vanishes without closing cleanly cancels the stream context and
// sends nothing at all. That has to unwind the handler on its own, with no
// shutdown involved, and it has to report the context error rather than nil:
// this stream really did fail, and the caller's logging depends on the
// difference.
func TestStreamContextCancellationReturnsTheContextError(t *testing.T) {
	s := NewReverseServer()

	ctx, cancel := context.WithCancel(context.Background())
	stream := newFakeStream(t).withContext(ctx)
	errCh := registerStream(t, s, stream, pb.Platform_PLATFORM_DISCORD)

	cancel()

	err := requireReturn(t, errCh, "OpenClientActionStream after its context was cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("handler returned %v, want context.Canceled", err)
	}
	if got := s.clientCount(); got != 0 {
		t.Errorf("clientCount() = %d after the handler returned, want 0", got)
	}

	// Cancellation is the second unwind path and it does not go through
	// s.done, so it gets the same receive-goroutine assertion as the shutdown
	// path: two ways out of the handler, two chances to abandon the goroutine.
	requireRecvGoroutineReleased(t, stream)
}

// ── A broken Send ────────────────────────────────────────────────────────────
//
// The sender goroutine returns when stream.Send fails, but nothing used to tell
// the handler. It stayed in Recv holding its registry slot and its
// clientSendBuffer-deep channel for the whole remaining life of the stream,
// while SendAction kept queueing actions into a channel with no reader — so the
// buffer filled and every later action for that platform was dropped, silently,
// against a client that was already gone.

// TestSendFailureDeregistersTheClient asserts the slot is RELEASED. A goroutine
// that returned is not the point; the slot it was still holding is.
func TestSendFailureDeregistersTheClient(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t).failSendsAfter(0, errors.New("transport is closing"))
	errCh := registerStream(t, s, stream, pb.Platform_PLATFORM_DISCORD)

	s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	// Waited on independently of the handler's return, so a handler that
	// unwinds past its deregistration fails here rather than passing.
	waitForClientCount(t, s, 0)
	requireReturn(t, errCh, "OpenClientActionStream after a failed Send")

	if got := stream.sentCount(); got != 0 {
		t.Errorf("stream recorded %d successful sends, want 0", got)
	}
}

// A Send broken on the FIRST action can be satisfied by a path that never really
// registered. Breaking a later one forces the healthy case to have happened
// first, so this is the test that proves the teardown works on a stream that was
// genuinely serving traffic.
func TestSendFailureOnALaterActionDeregistersTheClient(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t).failSendsAfter(1, errors.New("transport is closing"))
	errCh := registerStream(t, s, stream, pb.Platform_PLATFORM_DISCORD)

	s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))
	waitFor(t, func() bool { return stream.sentCount() == 1 })

	if got := s.clientCount(); got != 1 {
		t.Fatalf("clientCount() = %d after a successful send, want the client still registered", got)
	}

	s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	waitForClientCount(t, s, 0)
	requireReturn(t, errCh, "OpenClientActionStream after a failed Send")
}

// ── A sender stuck inside Send ───────────────────────────────────────────────
//
// The handler must wait for its sender before returning, because grpc-go forbids
// SendMsg after the handler has returned. Ordinarily deregister closing
// c.actions ends the sender's range and the wait is instant. The case that is
// not ordinary: a peer that has stopped reading with a full HTTP/2 flow-control
// window leaves the sender blocked INSIDE Send, nowhere near the range
// statement — and grpc-go only releases such a Send once the handler has
// returned. So an unbounded wait is a deadlock by construction, and under
// GracefulStop it pins process shutdown until the container runtime SIGKILLs the
// container.

// TestAStuckSenderDoesNotPinTheHandler is the only coverage of that bound. It
// asserts the three things that make the bound worth having: the handler
// returns, the registry slot it was holding is released, and the overrun is
// visible to an operator rather than silent.
func TestAStuckSenderDoesNotPinTheHandler(t *testing.T) {
	// Assigned rather than waited out. defaultSenderDrainTimeout is 5s, which is
	// most of this package's entire runtime for one assertion; the odd value is
	// so the overrun record below can be identified by it and not by matching on
	// wording.
	const drainTimeout = 37 * time.Millisecond

	s := NewReverseServer()
	s.senderDrainTimeout = drainTimeout

	stream := newFakeStream(t).blockSends()
	// Released before the test ends, so no goroutine is left parked in Send for
	// the rest of the binary — including when an assertion below fails.
	t.Cleanup(stream.releaseSends)

	errCh := registerStream(t, s, stream, pb.Platform_PLATFORM_DISCORD)

	logs.TakeAll()

	s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	// Inside Send, not merely scheduled. A sender still at the range statement
	// is released by deregister closing c.actions, which is the case that needs
	// no timeout at all and would make this test pass for the wrong reason.
	waitFor(t, stream.sendEntered)

	// An ordinary client disconnect. It closes c.actions by way of deregister,
	// and that does not reach a sender already blocked in Send.
	stream.closeRecv()

	if err := requireReturn(t, errCh, "OpenClientActionStream with its sender stuck in Send"); err != nil {
		t.Errorf("handler returned %v, want nil: an EOF from the client is not a stream failure", err)
	}

	// The slot, not the goroutine. The sender is deliberately still stuck; what
	// must not still be held is capacity another client needs.
	waitForClientCount(t, s, 0)

	overrun := false
	for _, entry := range logs.TakeAll() {
		if hasDurationField(entry, drainTimeout) {
			overrun = true
			break
		}
	}
	if !overrun {
		t.Errorf("giving up on the sender after %v was not recorded at warn level; "+
			"a goroutine knowingly left to outlive its handler has to be visible", drainTimeout)
	}
}

// ── A panic inside Recv ──────────────────────────────────────────────────────
//
// RecoverStreamInterceptor wraps the goroutine running the handler and nothing
// else, so it does not cover the goroutine the handler starts. That goroutine is
// where Recv now runs — protobuf unmarshalling plus a protovalidate CEL
// evaluation, over bytes from a peer this stream never authenticates — so a
// panic there is not one failed RPC, it is the whole server process, taking the
// database, the cron loop and every other service with it.

// TestAPanicInRecvBecomesInternalRatherThanKillingTheProcess: the panic arrives
// before any registration, which is the first thing a hostile peer can reach.
func TestAPanicInRecvBecomesInternalRatherThanKillingTheProcess(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t)

	errCh := openStream(s, stream)
	stream.panicNextRecv("malformed registration blew up in Recv")

	err := requireReturn(t, errCh, "OpenClientActionStream after Recv panicked")
	// Internal specifically: the peer gets no detail about what it broke, and a
	// reconnecting platform client reads it as an ordinary drop rather than as a
	// refusal it should back off from.
	requireCode(t, err, codes.Internal)
}

// TestAPanicInRecvReleasesTheRegistrySlot: recovering is not enough on its own.
// The handler's deferred deregistration has to run, or a peer that can provoke
// the panic at will burns one of maxStreamClients slots per attempt and locks
// the real platform clients out with 64 malformed messages.
func TestAPanicInRecvReleasesTheRegistrySlot(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t)

	// Registered first, and observed to be registered, so the assertion below is
	// about a slot that demonstrably existed.
	errCh := registerStream(t, s, stream, pb.Platform_PLATFORM_DISCORD)

	stream.panicNextRecv("second message blew up in Recv")

	err := requireReturn(t, errCh, "a registered OpenClientActionStream after Recv panicked")
	requireCode(t, err, codes.Internal)

	waitForClientCount(t, s, 0)
}
