package discord

import (
	"context"

	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

func healthCheckCommand() command.Command {
	return command.Command{
		Name:        "healthcheck",
		Aliases:     localizedAliases("healthcheck"),
		Description: "Health check of services such as DB.",
		Handler:     healthCheck,
	}
}

func healthCheck(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	resp, err := client.UtilityServiceClient.HealthCheck(ctx, pb.HealthCheckReq_builder{}.Build())
	if err != nil {
		log.Z.Error("failed to call HealthCheck.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content: resp.GetStatus().String(),
	}, nil
}
