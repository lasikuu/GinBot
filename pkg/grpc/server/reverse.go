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

// clientSendBuffer bounds how many actions may queue for a single client before
// further sends to it are dropped. A slow or wedged client must not be able to
// block the sender, which is what a shared unbuffered path would do.
const clientSendBuffer = 64

// defaultSenderDrainTimeout bounds how long a returning handler waits for its
// sender goroutine to finish.
//
// The wait itself is not optional, and the consequence of skipping it is worse
// on this transport than it was on grpc-go, where SendMsg after the handler
// returned merely returned an error. connect's connectStreamingHandlerConn.Send
// and .Close both write to the same underlying http.ResponseWriter with no
// synchronisation between them, and net/http2 does not tolerate the late write
// at all: http2.responseWriter.write PANICS with "Write called after Handler
// finished" once handlerDone has nilled the request state and returned it to a
// pool. So the sender must normally be done first, and when it is not, it needs
// the recover it carries below. But the wait cannot be unbounded either. A
// sender blocked inside stream.Send against a peer that
// has stopped reading, with a full HTTP/2 flow-control window, is not at the
// range statement — so deregister closing c.actions does not release it, and
// the handler would never return at all. Under http.Server.Shutdown that pins
// process shutdown until the container runtime SIGKILLs it.
//
// So the trade is explicit: five seconds is far longer than a healthy Send
// needs, and expiring it risks a goroutine that briefly outlives its handler
// rather than a shutdown that cannot complete.
//
// It is only the default for ReverseServer.senderDrainTimeout: asserting the
// expiry costs a real wait of exactly this long, so a test in this package
// shortens the field instead of sleeping it out.
const defaultSenderDrainTimeout = 5 * time.Second

// maxStreamClients caps how many client action streams may be registered at once.
//
// http2.Server.MaxConcurrentStreams is deliberately NOT the mechanism here: it
// is a per-CONNECTION limit, so N separate connections each opening a single
// stream sail straight past it. The unbounded resource is this server's
// registry — every admitted client costs a goroutine plus a
// clientSendBuffer-slot channel, held for the life of the stream — so the cap
// belongs on the registry.
//
// 64 is far above the real deployment, which has two platform clients. That
// headroom is for reconnect churn and rolling restarts, where the old stream's
// deregistration can lag the new stream's registration, while still bounding
// the cost at 64 goroutines and 64*64 queued action pointers.
//
// Precisely: it bounds REGISTERED clients. A stream that opens and never sends
// a registration message holds no slot and is not counted here — it is bounded
// instead by the clearance interceptor now refusing to open it at all without a
// registered caller, and by MaxConcurrentStreams per connection.
//
// OpenClientActionStream is guarded at CLEARANCE_REGISTERED like every other
// authenticated RPC (see interceptor.DefaultRequirements): ClearanceInterceptor
// runs on WrapStreamingHandler exactly as it does on the unary path, resolving
// and checking the caller before this handler ever sees the stream. That is a
// change from the grpc-go server this replaced, whose stream chain carried
// only recovery and validation. This cap is not standing in for that check —
// it never was the identity boundary — it exists purely to bound the registry
// once a caller has already passed clearance:
//
//   - Blast radius shrinks either way. Before this cap a registered caller
//     opening streams in a loop grew the registry until the process died,
//     which took the control channel down WITH the rest of the server.
//     Refusing at 64 is strictly the better of the two outcomes.
//   - Recovery is automatic, and the cost of it is bounded. A client refused
//     with the ResourceExhausted returned below treats that as a refusal rather
//     than a healthy drop, so each refusal DOUBLES its reconnect delay instead
//     of clearing it: one refusal costs one doubling, and five consecutive ones
//     — about a minute of accumulated waiting — reach the 30s ceiling. So a
//     sustained lockout settles at one retry per client every 30s rather than
//     one every second. The price is paid by the legitimate client that lost
//     its slot during a rolling restart: it re-registers on its own with no
//     operator action, but up to 30s after a slot actually frees rather than
//     immediately.
//
// Two narrower caps were considered and rejected. A per-peer cap keys on a
// remote address that is the proxy's, not the client's, behind any ingress or
// NAT — it would refuse legitimate clients in exactly the deployments that need
// it most. Evicting the oldest stream on the same platform trades a lockout for
// a flapping control channel, since a misbehaving registered caller simply
// re-registers and evicts the legitimate client straight back. See ADR-0012 for
// the identity model this cap sits on top of.
const maxStreamClients = 64

// streamClient is one connected platform client.
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

	// done is closed by Shutdown and selected on by every open handler. It is
	// never written to, only closed, so no lock guards it — closing a channel is
	// the one broadcast primitive that needs none.
	done         chan struct{}
	shutdownOnce sync.Once

	// senderDrainTimeout is read once per handler return and never written after
	// construction, so it needs no lock.
	senderDrainTimeout time.Duration
}

func NewReverseServer() *ReverseServer {
	s := &ReverseServer{
		clients: make(map[uint64]*streamClient),
		// Must be a real channel: a nil one blocks its select arm forever, so
		// Shutdown would silently never reach the handlers.
		done:               make(chan struct{}),
		senderDrainTimeout: defaultSenderDrainTimeout,
	}
	return s
}

// Shutdown causes every open OpenClientActionStream handler to return without
// waiting for a client message. Safe to call once; subsequent calls are no-ops.
func (s *ReverseServer) Shutdown() {
	s.shutdownOnce.Do(func() {
		close(s.done)
	})
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

// received is one result of a stream.Receive() call, moved off the handler's
// goroutine so the handler can select over it alongside everything else that
// should end the stream.
type received struct {
	msg *pb.OpenClientActionStreamReq
	err error
}

// OpenClientActionStream serves a bidirectional stream for one client.
//
// The client identifies its platform by sending a registration message; from
// then on it receives every action addressed to that platform.
//
// Receive deliberately does NOT drive the loop directly. Blocking the handler
// on it makes three separate conditions unobservable: server shutdown,
// cancellation of the stream's own context, and the failure of this stream's
// sender goroutine. All three have to end the handler, so they are select arms
// instead — four in total, the fourth being the received message itself.
func (s *ReverseServer) OpenClientActionStream(ctx context.Context, stream *connect.BidiStream[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp]) error {
	// The client's platform is not known until its first message arrives, so
	// registration is deferred until then.
	var (
		client     *streamClient
		deregister func()
		sendDone   chan struct{}
		// sendErr is written by the sender goroutine strictly BEFORE it closes
		// sendDone, and read here strictly AFTER a receive from sendDone. The
		// close is the happens-before edge, so this needs no lock and stays
		// clean under -race.
		//
		// That edge does NOT exist on the drain-timeout path below, where the
		// handler gives up on sendDone and returns while the sender is still
		// inside Send. Any write the sender makes here after that point has no
		// reader at all, which is why it is harmless. It stops being harmless
		// the moment anything reads sendErr without having received from
		// sendDone first: do NOT add a sendErr field to the deferred logging
		// below. That would be a genuine race, and one -race would almost never
		// report, since it needs Send blocked for the whole drain timeout.
		sendErr error
	)
	defer func() {
		if deregister == nil {
			return
		}

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

	// handlerDone releases the receive goroutine from a publish nobody will ever
	// read. It cannot release the goroutine from Receive itself: nothing here
	// cancels a server-side Receive directly, so after this handler returns the
	// goroutine may stay parked in Receive until the transport tears the stream
	// down (which ctx being cancelled — see the select below — brings forward
	// close to immediately in practice). That residual is precisely why
	// ReverseServer.Shutdown() must be called BEFORE http.Server.Shutdown() in
	// cmd/ginbot-server — Shutdown waits for in-flight handlers to return before
	// it lets the process exit, so the reverse order deadlocks: the handlers
	// wait for a client message that shutdown was supposed to make unnecessary.
	//
	// The goroutine carries its own recover because it sits OUTSIDE the
	// interceptor chain. RecoverInterceptor.WrapStreamingHandler unwinds only
	// the goroutine running next(ctx, conn), and this is not that goroutine, so
	// a panic here has no recovering frame above it and kills the whole process
	// — Postgres pool, cron loop and every other service with it. Assuming the
	// chain covers this is exactly the mistake to avoid: Receive is not inert,
	// it unmarshals and protovalidates bytes from a peer this method does not
	// authenticate beyond the clearance check already passed to reach here.
	handlerDone := make(chan struct{})
	recvCh := make(chan received, 1)
	defer close(handlerDone)

	go func() {
		// Converting the panic into a terminal received{} rather than just
		// logging it is what unwinds the handler through its ordinary error
		// path, so the deferred deregistration above still runs. The recovered
		// value can carry internal detail, so it goes to the log and the peer
		// gets a bare Internal.
		//
		// This cannot publish twice or wedge the handler: the panic escapes from
		// Receive, before the loop's own publish, and the loop is already
		// unwinding by the time this runs, so there is exactly one publish and
		// no further iteration. handlerDone covers the case where nobody is left
		// to read it.
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

			// An error is terminal for the stream, so there is nothing left to
			// read; continuing would spin on it forever.
			if err != nil {
				return
			}
		}
	}()

	// The status this returns on an abrupt client disconnect is deliberately
	// NON-DETERMINISTIC, and that is recorded here so it is not rediscovered as
	// a bug. One disconnect readies three of these arms at once: ctx is
	// cancelled, the same cancellation breaks a blocked Send so sendDone closes,
	// and the transport error also surfaces on recvCh. select picks uniformly
	// among ready arms, so the same event reports Canceled, the raw send error,
	// or the raw receive error from one run to the next — where blocking on
	// Receive always reported the receive error.
	//
	// Nothing depends on the distinction: the client buckets all three as an
	// ordinary drop, and the caller only logs it. Do NOT write a test asserting
	// a particular status on that path.
	for {
		select {
		case <-s.done:
			// Shutting down. Not an error: the stream ended because this server
			// asked it to.
			return nil

		case <-ctx.Done():
			return ctx.Err()

		case <-sendDone:
			// Nil until the client registers, and a select on a nil channel
			// blocks this arm forever — which is exactly right, since there is
			// no sender to fail before then. Reaching here means Send failed;
			// returning runs the deferred deregister, so the client does not
			// keep its registry slot and its buffer for a stream it can no
			// longer be written to.
			return sendErr

		case r := <-recvCh:
			if errors.Is(r.err, io.EOF) {
				return nil
			}
			if r.err != nil {
				return r.err
			}

			// Second line of defence, not the first. OpenClientActionStreamReq
			// now constrains platform_enum with required + defined_only, so the
			// validation interceptor rejects an unspecified platform at the edge
			// and this never fires through the server's own chain. It stays
			// because it is still reachable: unit tests drive this handler
			// directly with no chain around it, and an interceptor left off a
			// future wiring would otherwise register a client on a platform
			// nothing can ever route to.
			platform := r.msg.GetPlatformEnum()
			if platform == pb.Platform_PLATFORM_UNSPECIFIED {
				log.Z.Warn("ignoring stream registration with unspecified platform")
				continue
			}

			if client != nil {
				// Already registered. Re-registration is not meaningful, but a
				// client may legitimately keep the stream alive with further
				// messages.
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
				return connect.NewError(connect.CodeResourceExhausted, errors.New("too many client action streams"))
			}
			sendDone = make(chan struct{})

			go func(c *streamClient, done chan struct{}) {
				defer close(done)

				// This goroutine sits OUTSIDE the interceptor chain, exactly as
				// the receive goroutine above does, so nothing else recovers for
				// it and a panic here would kill the whole process — Postgres
				// pool, cron loop and every other stream with it.
				//
				// It is reachable: on the senderDrainTimeout path the handler
				// has already returned, and net/http2 panics with "Write called
				// after Handler finished" on any Send that lands after that.
				// Recovering turns a bounded, already-accepted trade — a
				// goroutine that briefly outlives its handler — back into what
				// the drain timeout's comment claims it costs.
				//
				// Nothing is published: by this point either the handler read
				// sendErr through the sendDone edge, or it gave up on the drain
				// and there is no reader at all.
				defer func() {
					if recovered := recover(); recovered != nil {
						log.Z.Error("recovered from a panic in a stream send goroutine",
							zap.Uint64("client_id", c.id),
							zap.Any("panic", recovered),
							zap.ByteString("stack", debug.Stack()),
						)
					}
				}()

				// Ranging over the channel exits when deregister closes it.
				for action := range c.actions {
					if err := stream.Send(action); err != nil {
						log.Z.Warn("failed to send action to client",
							zap.Uint64("client_id", c.id),
							zap.Error(err),
						)
						// Published to the handler by the deferred close above.
						sendErr = err
						return
					}
				}
			}(client, sendDone)
		}
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
