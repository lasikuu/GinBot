package server

import (
	"context"
	"errors"
	"io"
	"runtime/debug"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// clientSendBuffer bounds queued actions so a wedged client cannot block the sender.
const clientSendBuffer = 64

// defaultSenderDrainTimeout: net/http2 panics on a Send landing after the handler
// returned, but an unbounded wait would pin shutdown behind a stalled peer.
const defaultSenderDrainTimeout = 5 * time.Second

// maxStreamClients caps the registry; MaxConcurrentStreams is per-connection. See ADR-0012.
const maxStreamClients = 64

type streamClient struct {
	id       uint64
	platform pb.Platform
	actions  chan *pb.OpenClientActionStreamResp
}

type ReverseServer struct {
	ginbotv1connect.UnimplementedReverseServiceHandler

	mu      sync.RWMutex // protects clients and nextID
	clients map[uint64]*streamClient
	nextID  uint64

	// done is only ever closed, never sent to, so no lock guards it.
	done         chan struct{}
	shutdownOnce sync.Once

	// senderDrainTimeout is never written after construction, so it needs no lock.
	senderDrainTimeout time.Duration
}

func NewReverseServer() *ReverseServer {
	s := &ReverseServer{
		clients: make(map[uint64]*streamClient),
		// A nil channel would block Shutdown's broadcast forever.
		done:               make(chan struct{}),
		senderDrainTimeout: defaultSenderDrainTimeout,
	}
	return s
}

// Shutdown ends every open OpenClientActionStream handler. Idempotent.
func (s *ReverseServer) Shutdown() {
	s.shutdownOnce.Do(func() {
		close(s.done)
	})
}

// register reports ok false at capacity; the count check shares the insert's write lock.
func (s *ReverseServer) register(platform pb.Platform) (admitted *streamClient, deregister func(), ok bool) {
	s.mu.Lock()
	if len(s.clients) >= maxStreamClients {
		s.mu.Unlock()
		return nil, nil, false
	}

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
	}, true
}

type received struct {
	msg *pb.OpenClientActionStreamReq
	err error
}

// OpenClientActionStream serves a bidirectional stream for one client; the
// platform comes from the ginbot-platform-enum header, not the request body.
func (s *ReverseServer) OpenClientActionStream(ctx context.Context, stream *connect.BidiStream[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp]) error {
	meta, err := getMetadata(ctx)
	if err != nil {
		return err
	}

	// Unreachable today: callermeta.FromHeader already refuses an unspecified platform.
	if meta.PlatformEnum == pb.Platform_PLATFORM_UNSPECIFIED {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("platform is required"))
	}

	client, deregister, admitted := s.register(meta.PlatformEnum)
	if !admitted {
		log.Z.Warn("refusing client action stream, registry is at capacity",
			zap.String("platform", meta.PlatformEnum.String()),
			zap.Int("max_clients", maxStreamClients),
		)
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many client action streams"))
	}

	sendDone := make(chan struct{})
	// sendErr is written before sendDone closes and must only be read after a
	// receive from sendDone; that close is the only happens-before edge.
	var sendErr error

	defer func() {
		deregister()
		select {
		case <-sendDone:
		case <-time.After(s.senderDrainTimeout):
			log.Z.Warn("action sender did not finish within the drain timeout; letting it outlive the stream",
				zap.Uint64("client_id", client.id),
				zap.Duration("timeout", s.senderDrainTimeout),
			)
		}
	}()

	go func(c *streamClient, done chan struct{}) {
		defer close(done)

		// Outside the interceptor chain, so nothing else recovers: net/http2
		// panics on a Send landing after the drain timeout let the handler return.
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Z.Error("recovered from a panic in a stream send goroutine",
					zap.Uint64("client_id", c.id),
					zap.Any("panic", recovered),
					zap.ByteString("stack", debug.Stack()),
				)
			}
		}()

		for action := range c.actions {
			if err := stream.Send(action); err != nil {
				log.Z.Warn("failed to send action to client",
					zap.Uint64("client_id", c.id),
					zap.Error(err),
				)
				sendErr = err
				return
			}
		}
	}(client, sendDone)

	// Nothing unparks the receive goroutine from Receive, so ReverseServer.Shutdown()
	// must run before http.Server.Shutdown(), which waits on in-flight handlers.
	handlerDone := make(chan struct{})
	recvCh := make(chan received, 1)
	defer close(handlerDone)

	go func() {
		// Outside the interceptor chain; the panic becomes a terminal received{}.
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			log.Z.Error("recovered from a panic in a stream receive goroutine",
				zap.String("procedure", ginbotv1connect.ReverseServiceOpenClientActionStreamProcedure),
				zap.Any("panic", recovered),
				zap.ByteString("stack", debug.Stack()),
			)

			select {
			case recvCh <- received{err: connect.NewError(connect.CodeInternal, errors.New("internal error"))}:
			case <-handlerDone:
			}
		}()

		for {
			msg, err := stream.Receive()

			select {
			case recvCh <- received{msg: msg, err: err}:
			case <-handlerDone:
				return
			}

			// An error is terminal; continuing would spin on it forever.
			if err != nil {
				return
			}
		}
	}()

	// One disconnect readies ctx.Done, sendDone and recvCh at once, so the error
	// returned on that path is non-deterministic; do not assert a specific one.
	for {
		select {
		case <-s.done:
			return nil

		case <-ctx.Done():
			return ctx.Err()

		case <-sendDone:
			return sendErr

		case r := <-recvCh:
			if errors.Is(r.err, io.EOF) {
				return nil
			}
			if r.err != nil {
				return r.err
			}

			// The request has no fields; this arm only keeps Receive being called.
		}
	}
}

// SendAction never blocks: undeliverable actions are dropped.
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
