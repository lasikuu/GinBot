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

// ActionHandlers maps an action to its handler; an action with no entry is ignored.
type ActionHandlers map[pb.ClientAction]ActionHandler

const (
	reconnectMinBackoff = 1 * time.Second
	reconnectMaxBackoff = 30 * time.Second
)

const registrationTimeout = 10 * time.Second

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

// actionStream is the half of connect's bidi client stream this package uses;
// the seam exists because *connect.BidiStreamForClient needs a real server.
type actionStream interface {
	Send(*pb.OpenClientActionStreamReq) error
	Receive() (*pb.OpenClientActionStreamResp, error)
	CloseRequest() error
	CloseResponse() error
}

type actionStreamOpener func(context.Context) actionStream

// StreamIdentity is the bot process's own platform account, not any user's.
type StreamIdentity struct {
	Platform    pb.Platform
	PlatformUID string
	// Username is used only when registering for the first time.
	Username string
}

func (c *Clients) openActionStream(ctx context.Context) actionStream {
	return c.Reverse.OpenClientActionStream(ctx)
}

// RunClientActionStream keeps a client action stream open until ctx is
// cancelled, reconnecting with exponential backoff. It blocks.
func (c *Clients) RunClientActionStream(ctx context.Context, id StreamIdentity, handlers ActionHandlers) {
	// Attached here so every reconnect attempt carries the identity headers.
	ctx = callermeta.NewOutgoingContext(ctx, id.Platform, id.PlatformUID)

	ensure := func(ctx context.Context) error {
		return c.ensureRegistered(ctx, id)
	}

	runClientActionStream(ctx, c.openActionStream, ensure, id, handlers, time.After)
}

// runClientActionStream is the reconnect loop; its clock is a parameter so the
// delay schedule can be asserted without waiting it out.
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

		// Before the wait, so retry_in logged below is this iteration's delay.
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

// nextBackoff returns the delay before the next attempt. Only streamEstablished
// resets it: a refusal arrives on an opened stream, and treating that as a
// healthy drop would retry once a second for as long as the refusal lasts.
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

// runOnce registers this process if needed, opens a single stream, and pumps it
// until it fails or ctx is cancelled.
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
	// LIFO, so CloseRequest runs first, matching connect's generated client.
	defer stream.CloseResponse() //nolint:errcheck // best-effort on an attempt already being abandoned
	defer stream.CloseRequest()  //nolint:errcheck // best-effort on an attempt already being abandoned

	// Empty hello; the server reads the platform from headers. Still required:
	// connect issues the HTTP request lazily on the first Send.
	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		return streamUnreachable, err
	}

	log.Z.Info("client action stream opened", zap.String("platform", id.Platform.String()))

	for {
		in, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return streamEstablished, errors.New("server closed the action stream")
		}
		if err != nil {
			return recvOutcome(err), err
		}

		dispatch(streamCtx, in, handlers)
	}
}

// recvOutcome classifies a terminal stream error. These four codes mean the
// server declined this client, not that the connection failed; retrying sooner
// helps none of them. Everything else is an ordinary drop.
func recvOutcome(err error) streamOutcome {
	switch connect.CodeOf(err) {
	case connect.CodeResourceExhausted, connect.CodePermissionDenied, connect.CodeUnimplemented, connect.CodeFailedPrecondition:
		return streamRejected
	default:
		return streamEstablished
	}
}

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

	// Nothing above this frame recovers: the handler runs inline on the receive
	// loop's goroutine, so a panic here would kill the client process.
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

	// Inline: delivery ordering is meaningful; the server bounds in-flight
	// actions per client.
	handler(ctx, in)
}

// ensureRegistered registers this process's own platform identity, treating
// AlreadyExists as success. It runs before every stream attempt rather than
// latching, so a client whose account is deleted underneath it re-registers.
func (c *Clients) ensureRegistered(ctx context.Context, id StreamIdentity) error {
	callCtx, cancel := context.WithTimeout(ctx, registrationTimeout)
	defer cancel()

	callCtx = callermeta.NewOutgoingContext(callCtx, id.Platform, id.PlatformUID)

	username := id.Username
	req := connect.NewRequest(pb.RegisterReq_builder{Username: &username}.Build())

	_, err := c.User.Register(callCtx, req)
	switch {
	case err == nil:
		log.Z.Info("registered this process's own platform identity",
			zap.String("platform", id.Platform.String()))
	case connect.CodeOf(err) == connect.CodeAlreadyExists:
		// Debug, so a flapping connection does not fill the log.
		log.Z.Debug("this process's platform identity is already registered",
			zap.String("platform", id.Platform.String()))
	default:
		return err
	}

	return nil
}
