package server

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
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

// register sends a registration message and waits for the server to record it.
func (f *fakeStream) register(t *testing.T, s *ReverseServer, platform pb.Platform) {
	t.Helper()
	f.recv <- pb.OpenClientActionStreamReq_builder{PlatformEnum: platform.Enum()}.Build()
	waitFor(t, func() bool { return s.clientCount() > 0 || s.clientCountFor(platform) > 0 })
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

	for i, st := range streams {
		waitFor(t, func() bool { return st.sentCount() == 1 })
		if got := st.sentCount(); got != 1 {
			t.Errorf("stream %d received %d actions, want 1", i, got)
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
	client, deregister := s.register(pb.Platform_PLATFORM_DISCORD)
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
