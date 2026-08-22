package discord

import (
	"context"

	"github.com/lasikuu/GinBot/pkg/command"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/emptypb"
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
	resp, err := client.UtilityServiceClient.HealthCheck(ctx, &emptypb.Empty{})
	if err != nil {
		log.Z.Error("failed to call HealthCheck.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content: resp.GetStatus().String(),
	}, nil
}
