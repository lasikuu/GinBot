package client

import (
	"context"
	"errors"
	"io"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
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

const (
	reconnectMinBackoff = 1 * time.Second
	reconnectMaxBackoff = 30 * time.Second
)

// RunClientActionStream keeps a client action stream open until ctx is
// cancelled, reconnecting with exponential backoff when it drops.
//
// It blocks, so callers typically run it in its own goroutine.
func RunClientActionStream(ctx context.Context, platform pb.Platform, handlers ActionHandlers) {
	backoff := reconnectMinBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		established, err := runOnce(ctx, platform, handlers)
		if err != nil && ctx.Err() == nil {
			log.Z.Warn("client action stream dropped, reconnecting",
				zap.Duration("retry_in", backoff),
				zap.Bool("was_established", established),
				zap.Error(err),
			)
		}

		// A stream that actually connected clears the penalty. Without this the
		// backoff only ever grows, so a client that has dropped a few times over
		// its lifetime waits the maximum before every later reconnect, however
		// healthy the intervening connection was.
		if established {
			backoff = reconnectMinBackoff
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Only widen the delay for repeated *failures to connect*.
		if !established {
			backoff *= 2
			if backoff > reconnectMaxBackoff {
				backoff = reconnectMaxBackoff
			}
		}
	}
}

// runOnce opens a single stream and pumps it until it fails or ctx is cancelled.
// The bool reports whether the stream was successfully established, which
// distinguishes "cannot reach the server" from "connected, then dropped".
func runOnce(ctx context.Context, platform pb.Platform, handlers ActionHandlers) (bool, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := ReverseServiceClient.OpenClientActionStream(streamCtx)
	if err != nil {
		return false, err
	}

	// Register this client's platform so the server knows what to route here.
	req := pb.OpenClientActionStreamReq_builder{
		PlatformEnum: platform.Enum(),
	}.Build()
	if err := stream.Send(req); err != nil {
		return false, err
	}

	log.Z.Info("client action stream opened", zap.String("platform", platform.String()))

	for {
		in, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return true, errors.New("server closed the action stream")
		}
		if err != nil {
			return true, err
		}

		dispatch(streamCtx, in, handlers)
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

	// Handlers run inline: ordering is meaningful for notification delivery, and
	// the server already bounds how many actions can be in flight per client.
	handler(ctx, in)
}
