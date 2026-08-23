package server

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestMain(m *testing.M) {
	// The server logs unconditionally; give it a logger rather than a nil pointer.
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// fakeStream implements the server side of the bidi stream in memory.
type fakeStream struct {
	grpc.ServerStream

	recv chan *pb.OpenClientActionStreamReq

	mu   sync.Mutex
	sent []*pb.OpenClientActionStreamResp
}

func newFakeStream() *fakeStream {
	return &fakeStream{recv: make(chan *pb.OpenClientActionStreamReq, 4)}
}

func (f *fakeStream) Send(resp *pb.OpenClientActionStreamResp) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, resp)
	return nil
}

func (f *fakeStream) Recv() (*pb.OpenClientActionStreamReq, error) {
	req, ok := <-f.recv
	if !ok {
		return nil, io.EOF
	}
	return req, nil
}

func (f *fakeStream) Context() context.Context { return context.Background() }

func (f *fakeStream) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
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

	streams := []*fakeStream{newFakeStream(), newFakeStream(), newFakeStream()}
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
		close(st.recv)
	}
	wg.Wait()
}

// platformClients was written but never read, so routing did not exist.
func TestSendActionRoutesByPlatform(t *testing.T) {
	s := NewReverseServer()

	discordStream := newFakeStream()
	matrixStream := newFakeStream()

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

	close(discordStream.recv)
	close(matrixStream.recv)
	wg.Wait()
}

// The sender goroutine used to outlive the stream, leaking one per connection.
func TestClientIsDeregisteredOnDisconnect(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.OpenClientActionStream(stream)
	}()

	stream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()
	waitFor(t, func() bool { return s.clientCount() == 1 })

	close(stream.recv)
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
	stream := newFakeStream()

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

	close(stream.recv)
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

	stream := newFakeStream()
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
	close(stream.recv)
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
