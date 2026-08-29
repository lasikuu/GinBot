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
	"google.golang.org/protobuf/types/known/timestamppb"
)

// logs is installed once for the whole binary: log.Z is a global that handler
// goroutines read unsynchronised.
var logs *observer.ObservedLogs

var logCore zapcore.Core

func TestMain(m *testing.M) {
	logCore, logs = observer.New(zapcore.WarnLevel)
	log.Z = zap.New(logCore)
	log.S = log.Z.Sugar()
	m.Run()
}

// preserveObservedLogs re-attaches logCore after log.InitializeLogger replaces log.Z.
func preserveObservedLogs() {
	log.Z = zap.New(zapcore.NewTee(log.Z.Core(), logCore))
	log.S = log.Z.Sugar()
}

func (s *ReverseServer) clientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

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

func testAction(platform pb.Platform) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST
	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: platform.Enum(),
		ClientAction: &action,
		Test:         pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build(),
	}.Build()
}

const (
	reverseCallerUID    = "reverse-caller"
	reverseCallerUserID = "018f0000-0000-7000-8000-0000000000d0"
)

// reverseHarness registers one identity on both platforms at CLEARANCE_REGISTERED.
func reverseHarness(t *testing.T) *harness {
	t.Helper()

	user := testUser(reverseCallerUserID, pb.Clearance_CLEARANCE_REGISTERED)
	dir := newDirectory().
		add(pb.Platform_PLATFORM_DISCORD, reverseCallerUID, user).
		add(pb.Platform_PLATFORM_MATRIX_PROTOCOL, reverseCallerUID, user)

	return newHarness(t, withDirectory(dir))
}

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

// reverseStream is a bidi stream to Reverse. Cancelling its context deregisters
// server-side but does not unblock a pending Receive; unclosed, it hangs httptest.
type reverseStream struct {
	stream *connect.BidiStreamForClient[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp]
	rec    *streamRecorder
	done   <-chan struct{}
	cancel context.CancelFunc
}

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

// openRegisteredReverseClient opens a stream and waits until the server admits it.
func openRegisteredReverseClient(t *testing.T, h *harness, platform pb.Platform) *reverseStream {
	t.Helper()

	ctx, cancel := context.WithCancel(callerCtx(platform, reverseCallerUID))
	stream := h.Reverse.OpenClientActionStream(ctx)

	before := h.reverseServer.clientCount()
	// Connect issues the HTTP request lazily on the first Send, so the hello is required.
	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		cancel()
		t.Fatalf("send hello: %v", err)
	}

	// Registered before the wait: a t.Fatal there would skip the caller's own cleanup.
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

func requireStreamEnded(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not end within the deadline", what)
	}
}

func TestNewReverseServerDefaultsToTheDocumentedDrainTimeout(t *testing.T) {
	s := NewReverseServer()
	if s.senderDrainTimeout != defaultSenderDrainTimeout {
		t.Errorf("senderDrainTimeout = %v, want the documented default %v", s.senderDrainTimeout, defaultSenderDrainTimeout)
	}
}

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

	// Settle, then assert exactly one: waitFor cannot tell "one" from "one so far".
	time.Sleep(50 * time.Millisecond)
	for i, c := range clients {
		if got := c.rec.count(); got != 1 {
			t.Errorf("client %d received %d actions, want exactly 1", i, got)
		}
	}
}

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

func TestClientIsDeregisteredOnDisconnect(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	if err := c.stream.CloseRequest(); err != nil {
		t.Fatalf("CloseRequest: %v", err)
	}

	requireStreamEnded(t, c.done, "the client's receive loop after CloseRequest")
	waitForClientCount(t, h.reverseServer, 0)
}

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

// Asserted server-side: cancel does not unblock this client's own Receive.
func TestClientContextCancellationDeregistersTheClient(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	c.cancel()

	waitForClientCount(t, h.reverseServer, 0)
}

// Registered directly rather than through a stream, so nothing drains the buffer.
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
		for range clientSendBuffer * 3 {
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

func fillRegistry(t *testing.T, s *ReverseServer) {
	t.Helper()

	for i := range maxStreamClients {
		_, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)
		if !ok {
			t.Fatalf("register refused client %d of %d, before the cap was reached", i+1, maxStreamClients)
		}
		t.Cleanup(deregister)
	}
}

func TestRegisterAdmitsExactlyMaxStreamClients(t *testing.T) {
	s := NewReverseServer()
	fillRegistry(t, s)

	if got := s.clientCount(); got != maxStreamClients {
		t.Errorf("clientCount() = %d after %d successful registrations, want %d",
			got, maxStreamClients, maxStreamClients)
	}
}

// A deregister closure handed back by a refusal would close a channel it never owned.
func TestRegisterRefusesPastTheCapWithoutGrowingTheRegistry(t *testing.T) {
	s := NewReverseServer()
	fillRegistry(t, s)

	client, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)

	if ok {
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

// ResourceExhausted tells a reconnecting client to back off rather than re-register.
func TestOpenClientActionStreamPastTheCapIsResourceExhausted(t *testing.T) {
	h := reverseHarness(t)
	fillRegistry(t, h.reverseServer)

	ctx, cancel := context.WithCancel(callerCtx(pb.Platform_PLATFORM_DISCORD, reverseCallerUID))
	defer cancel()
	stream := h.Reverse.OpenClientActionStream(ctx)

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	_, err := stream.Receive()
	if err == nil {
		t.Fatal("the stream was admitted past the registry cap")
	}
	requireCode(t, err, connect.CodeResourceExhausted)

	if got := h.reverseServer.clientCount(); got != maxStreamClients {
		t.Errorf("clientCount() = %d after a refused stream, want it unchanged at %d", got, maxStreamClients)
	}
}

func TestACapacityFreedByADeregistrationIsReusable(t *testing.T) {
	s := NewReverseServer()

	// Registered manually rather than via fillRegistry: one is released mid-test.
	deregisters := make([]func(), 0, maxStreamClients)
	for i := range maxStreamClients {
		_, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)
		if !ok {
			t.Fatalf("register refused client %d of %d", i+1, maxStreamClients)
		}
		deregisters = append(deregisters, deregister)
	}
	defer func() {
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

// Check-then-insert outside the mutex would admit them all. Run under -race.
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
	for range attempts {
		done.Go(func() {
			start.Wait()

			client, deregister, ok := s.register(pb.Platform_PLATFORM_DISCORD)
			if !ok {
				// A non-nil closure here would double-free another client's channel.
				if client != nil || deregister != nil {
					badReturn.Add(1)
				}
				return
			}

			admitted.Add(1)
			mu.Lock()
			releases = append(releases, deregister)
			mu.Unlock()
		})
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

func TestShutdownUnblocksASilentRegisteredStream(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	h.reverseServer.Shutdown()

	requireStreamEnded(t, c.done, "OpenClientActionStream after Shutdown")

	waitForClientCount(t, h.reverseServer, 0)
}

func TestShutdownUnblocksAStreamRegisteredFromHeadersAloneBeforeAnyMessageContent(t *testing.T) {
	h := reverseHarness(t)

	ctx, cancel := context.WithCancel(callerCtx(pb.Platform_PLATFORM_DISCORD, reverseCallerUID))
	t.Cleanup(cancel)
	stream := h.Reverse.OpenClientActionStream(ctx)

	// A Connect client's first Send puts the request on the wire; nil sends headers only.
	if err := stream.Send(nil); err != nil {
		t.Fatalf("send headers-only: %v", err)
	}

	rec := &streamRecorder{}
	done := drainReverseStream(stream, rec)

	waitForClientCount(t, h.reverseServer, 1)

	h.reverseServer.Shutdown()

	requireStreamEnded(t, done, "a headers-only OpenClientActionStream after Shutdown")
	waitForClientCount(t, h.reverseServer, 0)
}

func TestClientRegisteredFromHeadersAloneReceivesActions(t *testing.T) {
	h := reverseHarness(t)

	ctx, cancel := context.WithCancel(callerCtx(pb.Platform_PLATFORM_DISCORD, reverseCallerUID))
	t.Cleanup(cancel)
	stream := h.Reverse.OpenClientActionStream(ctx)
	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})

	if err := stream.Send(nil); err != nil {
		t.Fatalf("send headers-only: %v", err)
	}

	rec := &streamRecorder{}
	drainReverseStream(stream, rec)

	waitForClientCount(t, h.reverseServer, 1)

	h.reverseServer.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	waitFor(t, func() bool { return rec.count() >= 1 })
}

func TestAnUnauthorisedCallerCannotOpenTheReverseStream(t *testing.T) {
	h := reverseHarness(t)

	ctx, cancel := context.WithCancel(anonymousCtx())
	defer cancel()
	stream := h.Reverse.OpenClientActionStream(ctx)

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	_, err := stream.Receive()
	if err == nil {
		t.Fatal("an anonymous caller was admitted to the reverse stream")
	}
	requireCode(t, err, connect.CodeInvalidArgument)

	if got := h.reverseServer.clientCount(); got != 0 {
		t.Errorf("clientCount() = %d, want 0: an unauthorised caller must not take a registry slot", got)
	}
}

// A second Shutdown is realistic, and closing an already-closed channel panics.
func TestShutdownIsIdempotent(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	h.reverseServer.Shutdown()
	h.reverseServer.Shutdown()
	h.reverseServer.Shutdown()

	requireStreamEnded(t, c.done, "OpenClientActionStream after repeated Shutdown")
}

func TestShutdownWithNoStreamsIsSafe(t *testing.T) {
	s := NewReverseServer()

	s.Shutdown()
	s.Shutdown()

	s.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))
}

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
