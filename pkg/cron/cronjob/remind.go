package cronjob

import (
	"context"

	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/emptypb"
)

func Remind() {
	resp, err := client.ReminderServiceClient.GetExpiredReminders(context.Background(), &emptypb.Empty{})
	if err != nil {
		log.Z.Error("failed to call GetExpiredReminders.", zap.Error(err))
		return
	}

	for _, reminder := range resp.GetReminders() {
		log.Z.Debug("reminding.", zap.Int64("id", *reminder.Id))
	}
}
