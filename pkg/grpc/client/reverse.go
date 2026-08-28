package client

import (
	"context"
	"errors"
	"io"
	"runtime/debug"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// ActionHandler handles one server-pushed action.
type ActionHandler func(context.Context, *pb.OpenClientActionStreamResp)

// ActionHandlers maps an action to its handler. Actions with no entry are logged
// and ignored.
type ActionHandlers map[pb.ClientAction]ActionHandler

// reconnectMinBackoff is the FLOOR, reached only by an established stream
// dropping — it is not the delay after a failed connect. runClientActionStream
// applies the transition before it waits, so the first failure already waits one
// doubling above the floor and the ladder starts at 2s. The names promise a
// little more than the loop delivers, which is why this says so.
const (
	reconnectMinBackoff = 1 * time.Second
	reconnectMaxBackoff = 30 * time.Second
)

// registrationTimeout bounds ensureRegistered's own Register call, run inside
// the reconnect loop before every attempt to open the stream until it
// succeeds once.
const registrationTimeout = 10 * time.Second

// streamOutcome distinguishes the three reasons a stream attempt ended, because
// only one of them should reset the reconnect backoff.
type streamOutcome int

const (
	streamUnreachable streamOutcome = iota // never established
	streamEstablished                      // connected, then dropped — reset backoff
	streamRejected                         // server refused — do NOT reset backoff
)

func (o streamOutcome) String() string {
	switch o {
	case streamUnreachable:
		return "unreachable"
	case streamEstablished:
		return "established"
	case streamRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// actionStream is the half of connect's bidi client stream this package uses.
// *connect.BidiStreamForClient[pb.OpenClientActionStreamReq,
// pb.OpenClientActionStreamResp] satisfies it structurally. It exists as its
// own interface because that generated type is a concrete struct: without
// this seam, runOnce could only be driven against a live server, and the
// reconnect ladder — the entire point of this file — could not be tested at
// all.
type actionStream interface {
	Send(*pb.OpenClientActionStreamReq) error
	Receive() (*pb.OpenClientActionStreamResp, error)
	CloseRequest() error
	CloseResponse() error
}

// actionStreamOpener opens one stream attempt.
type actionStreamOpener func(context.Context) actionStream

// StreamIdentity is the identity a platform client asserts to open the
// reverse action stream.
//
// It is the BOT PROCESS's own platform account, not any user's: the stream is
// long-lived and shared across every action pushed to this client, so there is
// no single invoking user to attribute it to.
type StreamIdentity struct {
	Platform    pb.Platform
	PlatformUID string
	// Username is used only when registering for the first time — see
	// ensureRegistered.
	Username string
}

// openActionStream adapts Reverse.OpenClientActionStream to actionStreamOpener.
func (c *Clients) openActionStream(ctx context.Context) actionStream {
	return c.Reverse.OpenClientActionStream(ctx)
}

// RunClientActionStream keeps a client action stream open until ctx is
// cancelled, reconnecting with exponential backoff when it drops.
//
// It blocks, so callers typically run it in its own goroutine.
func (c *Clients) RunClientActionStream(ctx context.Context, id StreamIdentity, handlers ActionHandlers) {
	// Attached here so callermeta.NewClientInterceptor puts the identity
	// headers on every attempt this loop makes, including the ones after a
	// reconnect — the ctx a caller passes in is not expected to carry it
	// already.
	ctx = callermeta.NewOutgoingContext(ctx, id.Platform, id.PlatformUID)

	ensure := func(ctx context.Context) error {
		return c.ensureRegistered(ctx, id)
	}

	runClientActionStream(ctx, c.openActionStream, ensure, id, handlers, time.After)
}

// runClientActionStream is the reconnect loop itself, taking its clock as a
// parameter so the delay schedule can be asserted without waiting it out.
//
// Unexported on purpose, like pkg/cron's runCronJobs. wait exists so the ladder
// can be driven without the production clock, not as a way for a caller to
// supply its own — cmd/ginbot-discord and cmd/ginbot-matrix reach this through
// Clients.RunClientActionStream and nothing else.
func runClientActionStream(
	ctx context.Context,
	open actionStreamOpener,
	ensure func(context.Context) error,
	id StreamIdentity,
	handlers ActionHandlers,
	wait func(time.Duration) <-chan time.Time,
) {
	backoff := reconnectMinBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		outcome, err := runOnce(ctx, open, ensure, id, handlers)

		// Applied before the wait rather than after it, which is a DELIBERATE
		// change to the delays actually taken. When every attempt fails to
		// connect the ladder used to be 1s 2s 4s 8s 16s 30s and is now 2s 4s 8s
		// 16s 30s — every delay shifted one step, so a client started before the
		// server (the ordinary `docker compose up` order) waits 2s rather than 1s
		// for its first retry.
		//
		// That is accepted in exchange for two things. There is now a single
		// point where the backoff transitions, instead of a reset before the wait
		// and an escalation after it. And retry_in below is finally the delay
		// this iteration goes on to take: escalating afterwards logged the
		// pre-escalation value, so the field did not describe the wait.
		backoff = nextBackoff(backoff, outcome)

		if err != nil && ctx.Err() == nil {
			log.Z.Warn("client action stream dropped, reconnecting",
				zap.Duration("retry_in", backoff),
				zap.String("outcome", outcome.String()),
				zap.Error(err),
			)
		}

		select {
		case <-ctx.Done():
			return
		case <-wait(backoff):
		}
	}
}

// nextBackoff returns the delay to wait before the next connection attempt.
//
// Only streamEstablished clears the accumulated penalty. Without that reset the
// backoff would only ever grow, so a client that had dropped a few times over
// its lifetime would wait the maximum before every later reconnect however
// healthy the intervening connection was.
//
// streamRejected escalates rather than resets, which is the whole point of
// separating it from streamEstablished: a refusal arrives from Recv on a stream
// that did open, so treating it as a healthy drop retried a refused client once
// per second for as long as the refusal lasted.
func nextBackoff(current time.Duration, outcome streamOutcome) time.Duration {
	if outcome == streamEstablished {
		return reconnectMinBackoff
	}

	next := current * 2
	if next > reconnectMaxBackoff {
		return reconnectMaxBackoff
	}

	return next
}

// runOnce registers this process once if needed, opens a single stream, and
// pumps it until it fails or ctx is cancelled.
func runOnce(
	ctx context.Context,
	open actionStreamOpener,
	ensure func(context.Context) error,
	id StreamIdentity,
	handlers ActionHandlers,
) (streamOutcome, error) {
	if err := ensure(ctx); err != nil {
		return streamUnreachable, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream := open(streamCtx)
	// Deferred so a stream that opened but never got past registration — a
	// failed Send below — still has its request and response halves released,
	// rather than leaking until the underlying connection itself is torn down.
	// Deferred, so LIFO: CloseRequest runs first, then CloseResponse. That is
	// the order connect's own generated client uses; neither deadlocks here,
	// but matching the library's convention costs nothing.
	defer stream.CloseResponse() //nolint:errcheck // best-effort on an attempt already being abandoned
	defer stream.CloseRequest()  //nolint:errcheck // best-effort on an attempt already being abandoned

	// This is a hello, not a registration: OpenClientActionStreamReq carries no
	// fields, and the server now takes this client's platform from the
	// ginbot-platform-enum header that callermeta.NewClientInterceptor stamps
	// on every attempt (see RunClientActionStream's ctx setup above). The Send
	// still has to happen, though — connect issues the underlying HTTP request
	// lazily, on the first Send or CloseRequest, so a client that never sent
	// anything would never reach the server handler at all and the stream
	// would silently never open.
	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		return streamUnreachable, err
	}

	log.Z.Info("client action stream opened", zap.String("platform", id.Platform.String()))

	for {
		in, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			// A server that closed the stream cleanly was reachable and did
			// serve us, so this counts as established.
			return streamEstablished, errors.New("server closed the action stream")
		}
		if err != nil {
			return recvOutcome(err), err
		}

		dispatch(streamCtx, in, handlers)
	}
}

// recvOutcome classifies a terminal stream error as a refusal or an ordinary
// drop.
//
// The distinction is not visible from the transport: a server-side refusal is
// delivered as the stream's terminal status, so it surfaces here on a stream
// that was opened and written to successfully. Reading that as "connected, then
// dropped" is what reset the backoff and turned a refusal into a once-a-second
// retry loop.
//
// Four codes mean the SERVER declined this client rather than the connection
// failing, and none of them is improved by retrying sooner:
//
//   - ResourceExhausted — the maxStreamClients registry cap on the server.
//   - PermissionDenied — an identity or clearance check rejecting this client.
//   - Unimplemented — this server does not serve ReverseService at all.
//   - FailedPrecondition — "caller is not registered"
//     (pkg/grpc/interceptor.ClearanceInterceptor.resolveCaller). This is
//     reachable only when the account existed and then went away, since a
//     registration that never succeeded returns streamUnreachable from
//     runOnce without ever opening a stream. Retrying sooner does not fix it;
//     re-registering does, which is why ensureRegistered runs on every
//     attempt rather than latching after its first success. Escalating the
//     backoff here is what stops a client whose row was dropped from
//     reconnecting once a second while it waits for the re-registration on
//     the next attempt to take effect.
//
// Everything else, io.EOF included, is treated as an ordinary drop worth an
// immediate reconnect.
//
// This used to run through google.golang.org/grpc/status.Code, which yields
// codes.Unknown for a *connect.Error — every refusal would have fallen through
// to streamEstablished, reset the backoff, and reintroduced the
// once-a-second retry loop. connect.CodeOf is the Connect-native equivalent:
// it type-asserts the error to *connect.Error and reports its Code, or
// CodeUnknown for anything else — including a plain context or transport
// error, which correctly falls through to the ordinary-drop branch below.
func recvOutcome(err error) streamOutcome {
	switch connect.CodeOf(err) {
	case connect.CodeResourceExhausted, connect.CodePermissionDenied, connect.CodeUnimplemented, connect.CodeFailedPrecondition:
		return streamRejected
	default:
		return streamEstablished
	}
}

// dispatch routes a received action to its registered handler.
func dispatch(ctx context.Context, in *pb.OpenClientActionStreamResp, handlers ActionHandlers) {
	action := in.GetClientAction()

	handler, ok := handlers[action]
	if !ok {
		log.Z.Warn("no handler registered for client action",
			zap.String("client_action", action.String()),
			zap.String("platform", in.GetPlatformEnum().String()),
		)
		return
	}

	log.Z.Debug("handling client action",
		zap.String("client_action", action.String()),
		zap.String("platform", in.GetPlatformEnum().String()),
	)

	// Nothing above this frame recovers: the handler runs inline on the stream's
	// receive loop, which runs on a bare goroutine started by
	// RunClientActionStream's caller. So without this a panic in any one action
	// handler takes the whole client process down, killing every other platform
	// capability with it. One bad action must cost one delivery instead.
	//
	// The stack is logged alongside the recovered value because the value on its
	// own — usually a bare "runtime error: invalid memory address" — does not say
	// which handler produced it.
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Z.Error("recovered from a panic in a client action handler",
				zap.String("client_action", action.String()),
				zap.String("platform", in.GetPlatformEnum().String()),
				zap.Any("panic", recovered),
				zap.ByteString("stack", debug.Stack()),
			)
		}
	}()

	// Handlers run inline: ordering is meaningful for notification delivery, and
	// the server already bounds how many actions can be in flight per client.
	handler(ctx, in)
}

// ensureRegistered registers this process's own platform identity.
// connect.CodeAlreadyExists is treated as success: another instance of this
// same client, or a previous run of this one, already created the account.
//
// It runs on EVERY stream attempt rather than latching after the first
// success, and that is deliberate. A latch is the obvious optimisation and it
// is wrong: the account can go away underneath a long-lived client — a
// `docker compose down -v`, a database restore, an operator deleting the row —
// after which the server refuses the stream with FailedPrecondition
// ("caller is not registered", pkg/grpc/interceptor.ClearanceInterceptor),
// recvOutcome correctly stops the reconnect loop hot-looping, and a latched
// client then never re-registers. Reminder delivery is dead until someone
// restarts the process, with nothing in the logs saying why.
//
// The cost of not latching is one extra unary call per stream ATTEMPT, not per
// action: attempts happen only when the stream drops, and the reconnect
// backoff already spaces them from 2s out to 30s. Against that, this is the
// difference between a client that heals itself and one that needs an
// operator.
//
// It runs inside the reconnect loop, before every attempt to open the stream,
// rather than once at dial time — cmd/ginbot-discord and cmd/ginbot-matrix
// routinely start before cmd/ginbot-server is reachable at all (the ordinary
// `docker compose up` order), so a registration attempted only at dial time
// would have nothing to register against.
//
// The reverse action stream is guarded at CLEARANCE_REGISTERED
// (pkg/grpc/interceptor.DefaultRequirements), and a bot process has no
// user_account row of its own until this runs — so without it the stream is
// permanently refused and reminder delivery never works at all.
func (c *Clients) ensureRegistered(ctx context.Context, id StreamIdentity) error {
	callCtx, cancel := context.WithTimeout(ctx, registrationTimeout)
	defer cancel()

	callCtx = callermeta.NewOutgoingContext(callCtx, id.Platform, id.PlatformUID)

	username := id.Username
	req := connect.NewRequest(pb.RegisterReq_builder{Username: &username}.Build())

	_, err := c.User.Register(callCtx, req)
	switch {
	case err == nil:
		// Info rather than Debug because this is the interesting one: it means
		// no account existed a moment ago. On a healthy client it happens once
		// in the process's life.
		log.Z.Info("registered this process's own platform identity",
			zap.String("platform", id.Platform.String()))
	case connect.CodeOf(err) == connect.CodeAlreadyExists:
		// The ordinary case on every reconnect after the first. Debug, so a
		// flapping connection does not fill the log with it.
		log.Z.Debug("this process's platform identity is already registered",
			zap.String("platform", id.Platform.String()))
	default:
		return err
	}

	return nil
}
