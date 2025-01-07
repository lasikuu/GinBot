package cron

import (
	"time"

	"github.com/lasikuu/GinBot/pkg/cron/cronjob"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

func RunCronJobs() {
	ticker := time.NewTicker(1 * time.Second)
	started := time.Now()

	for now := range ticker.C {

		cronjob.Remind()

		// Every minute
		if now.Second() == 0 {
			log.Z.Debug("cron running", zap.Time("time", now))

			// Testing
			platformEnum := pb.Platform_PLATFORM_DISCORD
			clientAction := pb.ClientAction_CLIENT_ACTION_SEND_TEST
			content := structpb.Struct{
				Fields: map[string]*structpb.Value{
					"test": structpb.NewStringValue("test"),
				},
			}

			resp := pb.OpenClientActionStreamResp_builder{
				PlatformEnum: &platformEnum,
				ClientAction: &clientAction,
				Content:      &content,
			}.Build()
			service.ReverseServer.SendAction(resp)

			cronjob.CongratulateBirthday()
		}

		// Every hour
		if now.Minute() == 0 && now.Second() == 0 {
		}

		if now.Sub(started) >= time.Hour*24 {
			// Resets the ticker after 24 hours
			ticker.Reset(1 * time.Second)
		}
	}
}
