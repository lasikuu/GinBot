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

		if err := runOnce(ctx, platform, handlers); err != nil && ctx.Err() == nil {
			log.Z.Warn("client action stream dropped, reconnecting",
				zap.Duration("retry_in", backoff),
				zap.Error(err),
			)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > reconnectMaxBackoff {
			backoff = reconnectMaxBackoff
		}
	}
}

// runOnce opens a single stream and pumps it until it fails or ctx is cancelled.
func runOnce(ctx context.Context, platform pb.Platform, handlers ActionHandlers) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := ReverseServiceClient.OpenClientActionStream(streamCtx)
	if err != nil {
		return err
	}

	// Register this client's platform so the server knows what to route here.
	req := pb.OpenClientActionStreamReq_builder{
		PlatformEnum: platform.Enum(),
	}.Build()
	if err := stream.Send(req); err != nil {
		return err
	}

	log.Z.Info("client action stream opened", zap.String("platform", platform.String()))

	for {
		in, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return errors.New("server closed the action stream")
		}
		if err != nil {
			return err
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
