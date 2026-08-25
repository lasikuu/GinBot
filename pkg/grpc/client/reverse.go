package client

import (
	"context"
	"errors"
	"io"
	"runtime/debug"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ReverseServiceClient pb.ReverseServiceClient

func InitReverseService(conn *grpc.ClientConn) {
	ReverseServiceClient = pb.NewReverseServiceClient(conn)
}

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

// RunClientActionStream keeps a client action stream open until ctx is
// cancelled, reconnecting with exponential backoff when it drops.
//
// It blocks, so callers typically run it in its own goroutine.
func RunClientActionStream(ctx context.Context, platform pb.Platform, handlers ActionHandlers) {
	runClientActionStream(ctx, platform, handlers, time.After)
}

// runClientActionStream is the reconnect loop itself, taking its clock as a
// parameter so the delay schedule can be asserted without waiting it out.
//
// Unexported on purpose, like pkg/cron's runCronJobs. wait exists so the ladder
// can be driven without the production clock, not as a way for a caller to
// supply its own — cmd/ginbot-discord and cmd/ginbot-matrix reach this through
// RunClientActionStream and nothing else.
func runClientActionStream(
	ctx context.Context,
	platform pb.Platform,
	handlers ActionHandlers,
	wait func(time.Duration) <-chan time.Time,
) {
	backoff := reconnectMinBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		outcome, err := runOnce(ctx, platform, handlers)

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

// runOnce opens a single stream and pumps it until it fails or ctx is cancelled.
func runOnce(ctx context.Context, platform pb.Platform, handlers ActionHandlers) (streamOutcome, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := ReverseServiceClient.OpenClientActionStream(streamCtx)
	if err != nil {
		return streamUnreachable, err
	}

	// Register this client's platform so the server knows what to route here.
	req := pb.OpenClientActionStreamReq_builder{
		PlatformEnum: platform.Enum(),
	}.Build()
	if err := stream.Send(req); err != nil {
		return streamUnreachable, err
	}

	log.Z.Info("client action stream opened", zap.String("platform", platform.String()))

	for {
		in, err := stream.Recv()
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

// recvOutcome classifies an error from Recv as a refusal or an ordinary drop.
//
// The distinction is not visible from the transport: a server-side refusal is
// delivered as the stream's terminal status, so it surfaces here on a stream
// that was opened and written to successfully. Reading that as "connected, then
// dropped" is what reset the backoff and turned a refusal into a once-a-second
// retry loop.
//
// Three codes mean the SERVER declined this client rather than the connection
// failing, and none of them is improved by retrying sooner:
//
//   - ResourceExhausted — the maxStreamClients registry cap on the server.
//   - PermissionDenied — an identity or clearance check rejecting this client.
//   - Unimplemented — this server does not serve ReverseService at all.
//
// Everything else, io.EOF included, is treated as an ordinary drop worth an
// immediate reconnect.
//
// This is the ONE seam the move to connectrpc.com/connect has to revisit. The
// classification runs through google.golang.org/grpc/status.Code, which yields
// codes.Unknown for a *connect.Error — so every refusal would fall through to
// streamEstablished, reset the backoff, and bring the once-a-second retry loop
// straight back. Deliberately left transport-specific until then rather than
// abstracted speculatively.
func recvOutcome(err error) streamOutcome {
	switch status.Code(err) {
	case codes.ResourceExhausted, codes.PermissionDenied, codes.Unimplemented:
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
