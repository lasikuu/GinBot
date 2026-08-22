package server

import (
	"errors"
	"io"
	"sync"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// clientSendBuffer bounds how many actions may queue for a single client before
// further sends to it are dropped. A slow or wedged client must not be able to
// block the sender, which is what a shared unbuffered path would do.
const clientSendBuffer = 64

// streamClient is one connected platform client.
type streamClient struct {
	id       uint64
	platform pb.Platform
	actions  chan *pb.OpenClientActionStreamResp
}

type ReverseServer struct {
	pb.UnimplementedReverseServiceServer

	mu      sync.RWMutex // protects clients and nextID
	clients map[uint64]*streamClient
	nextID  uint64
}

func NewReverseServer() *ReverseServer {
	s := &ReverseServer{
		clients: make(map[uint64]*streamClient),
	}
	return s
}

// register adds a client and returns it along with a function that removes it.
func (s *ReverseServer) register(platform pb.Platform) (*streamClient, func()) {
	s.mu.Lock()
	s.nextID++
	c := &streamClient{
		id:       s.nextID,
		platform: platform,
		actions:  make(chan *pb.OpenClientActionStreamResp, clientSendBuffer),
	}
	s.clients[c.id] = c
	s.mu.Unlock()

	log.Z.Info("client action stream registered",
		zap.Uint64("client_id", c.id),
		zap.String("platform", platform.String()),
	)

	return c, func() {
		s.mu.Lock()
		delete(s.clients, c.id)
		s.mu.Unlock()
		close(c.actions)

		log.Z.Info("client action stream deregistered",
			zap.Uint64("client_id", c.id),
			zap.String("platform", platform.String()),
		)
	}
}

// OpenClientActionStream serves a bidirectional stream for one client.
//
// The client identifies its platform by sending a registration message; from
// then on it receives every action addressed to that platform.
func (s *ReverseServer) OpenClientActionStream(stream pb.ReverseService_OpenClientActionStreamServer) error {
	// The client's platform is not known until its first message arrives, so
	// registration is deferred until then.
	var (
		client     *streamClient
		deregister func()
		sendDone   chan struct{}
	)
	defer func() {
		if deregister != nil {
			deregister()
			// Wait for the sender to observe the closed channel before returning,
			// so the goroutine cannot outlive the stream.
			<-sendDone
		}
	}()

	for {
		in, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		platform := in.GetPlatformEnum()
		if platform == pb.Platform_PLATFORM_UNSPECIFIED {
			log.Z.Warn("ignoring stream registration with unspecified platform")
			continue
		}

		if client != nil {
			// Already registered. Re-registration is not meaningful, but a client
			// may legitimately keep the stream alive with further messages.
			if platform != client.platform {
				log.Z.Warn("client attempted to change platform on an open stream",
					zap.Uint64("client_id", client.id),
					zap.String("from", client.platform.String()),
					zap.String("to", platform.String()),
				)
			}
			continue
		}

		client, deregister = s.register(platform)
		sendDone = make(chan struct{})

		go func(c *streamClient, done chan struct{}) {
			defer close(done)
			// Ranging over the channel exits when deregister closes it.
			for action := range c.actions {
				if err := stream.Send(action); err != nil {
					log.Z.Warn("failed to send action to client",
						zap.Uint64("client_id", c.id),
						zap.Error(err),
					)
					return
				}
			}
		}(client, sendDone)
	}
}

// SendAction delivers an action to every connected client on its target platform.
//
// Actions for a platform with no connected clients are dropped, as are actions
// for a client whose buffer is full; neither blocks the caller.
func (s *ReverseServer) SendAction(action *pb.OpenClientActionStreamResp) {
	target := action.GetPlatformEnum()

	s.mu.RLock()
	defer s.mu.RUnlock()

	var delivered, dropped int
	for _, c := range s.clients {
		if c.platform != target {
			continue
		}

		select {
		case c.actions <- action:
			delivered++
		default:
			dropped++
			log.Z.Warn("dropping action, client send buffer is full",
				zap.Uint64("client_id", c.id),
				zap.String("platform", target.String()),
			)
		}
	}

	if delivered == 0 && dropped == 0 {
		log.Z.Debug("no clients connected for action",
			zap.String("platform", target.String()),
			zap.String("action", action.GetClientAction().String()),
		)
	}
}
