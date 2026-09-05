package discord

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// healthCheckTimeout bounds the outgoing HealthCheck call. commandContext
// roots the handler context at context.Background with no deadline of its
// own, and a health check answering slowly is exactly the case this command
// exists to surface — it must not itself hang indefinitely waiting for one.
const healthCheckTimeout = 10 * time.Second

func healthCheckCommand() command.Command {
	return command.Command{
		Name:        "healthcheck",
		Aliases:     chatAliases("healthcheck"),
		Description: "Health check of services such as DB.",
		Handler:     healthCheck,
	}
}

func healthCheck(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	callCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()

	resp, err := clientsFrom(ctx).Utility.HealthCheck(callCtx, connect.NewRequest(pb.HealthCheckReq_builder{}.Build()))
	if err != nil {
		log.Z.Error("failed to call HealthCheck.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content: resp.Msg.GetStatus().String(),
	}, nil
}
