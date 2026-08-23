package server

import (
	"errors"
	"io"
	"sync"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// clientSendBuffer bounds how many actions may queue for a single client before
// further sends to it are dropped. A slow or wedged client must not be able to
// block the sender, which is what a shared unbuffered path would do.
const clientSendBuffer = 64

// maxStreamClients caps how many client action streams may be registered at once.
//
// grpc.MaxConcurrentStreams is deliberately NOT the mechanism here: it is a
// per-CONNECTION limit, so N separate connections each opening a single stream
// sail straight past it. The unbounded resource is this server's registry —
// every admitted client costs a goroutine plus a clientSendBuffer-slot channel,
// held for the life of the stream — so the cap belongs on the registry.
//
// 64 is far above the real deployment, which has two platform clients. That
// headroom is for reconnect churn and rolling restarts, where the old stream's
// deregistration can lag the new stream's registration, while still bounding
// the cost at 64 goroutines and 64*64 queued action pointers.
//
// What this cap does NOT fix, stated plainly because the cap is easy to mistake
// for a complete answer: OpenClientActionStream has no identity check at all.
// cmd/ginbot-server installs only recovery and validation on the stream chain —
// the clearance interceptor is unary-only — so with GINBOT_GRPC_TLS off,
// anything that can reach the port can hold all 64 slots and keep the real
// platform clients out for as long as it cares to. The consequences are bounded
// but real:
//
//   - Blast radius shrinks either way. Before this cap the same attacker grew
//     the registry until the process died, which took the control channel down
//     WITH the rest of the server. Refusing at 64 is strictly the better of the
//     two outcomes, not a new exposure.
//   - Recovery is automatic. client.RunClientActionStream retries indefinitely
//     with capped backoff, so a legitimate client re-registers on its own the
//     moment a slot frees; no operator action is required.
//   - Mutual TLS closes it properly. Under GINBOT_GRPC_TLS=true nothing without
//     a client certificate connects at all, so the exposure exists only in the
//     insecure default that is meant for local development.
//
// Two narrower caps were considered and rejected. A per-peer cap keys on a
// remote address that is the proxy's, not the client's, behind any ingress or
// NAT — it would refuse legitimate clients in exactly the deployments that need
// it most. Evicting the oldest stream on the same platform trades a lockout for
// a flapping control channel, since the attacker simply re-registers and evicts
// the legitimate client straight back. Neither addresses the actual hole. The
// real fix is a stream-side identity check along the lines of ADR-0012, which
// is a separate change with its own decision to record.
const maxStreamClients = 64

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

// register admits a client and returns it along with a function that removes
// it. ok is false when maxStreamClients is already reached, in which case
// nothing was mutated and both other results are nil.
//
// The admission check runs under the same write lock as the insert. Checking
// the count outside the lock and taking it inside would let concurrent
// registrations both observe a count below the cap and both insert.
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

		var admitted bool
		client, deregister, admitted = s.register(platform)
		if !admitted {
			log.Z.Warn("refusing client action stream, registry is at capacity",
				zap.String("platform", platform.String()),
				zap.Int("max_clients", maxStreamClients),
			)
			return status.Errorf(codes.ResourceExhausted, "too many client action streams")
		}
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
