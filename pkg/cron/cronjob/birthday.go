package cronjob

import (
	"context"
	"github.com/lasikuu/GinBot/pkg/discord"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/emptypb"
)

func CongratulateBirthday() {
	resp, err := discord.UserServiceClient.GetCongratulableBirthdays(context.Background(), &emptypb.Empty{})
	if err != nil {
		log.Z.Error("failed to call GetExpiredReminders.", zap.Error(err))
		return
	}

	for _, user := range resp.GetUsers() {
		log.Z.Debug("congratulating.", zap.String("userID", *user.Id))
	}
}
