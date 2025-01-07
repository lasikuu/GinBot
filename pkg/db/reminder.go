package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

func CreateReminder(req *pb.CreateReminderReq, userID string, destinationID int64) (string, error) {
	reminderUUID, err := uuid.NewV7()
	if err != nil {
		log.Z.Error("failed to generate UUID.", zap.Error(err))
		return "", err
	}
	reminderID := reminderUUID.String()

	_, err = db().Exec(
		context.Background(),
		"INSERT INTO reminder (id, datetime, timezone, repeat_cron, destination_id, message, user_id, parent_id) values($1, $2, $3, $4, $5, $6, $7)",
		reminderID, req.GetDatetime(), req.GetTimezone(), req.GetRepeatCron(), destinationID, req.GetMessage(), userID, req.GetParentId(),
	)
	if err != nil {
		log.Z.Error("failed to insert reminder.", zap.Error(err))
		return "", err
	}

	return reminderID, err
}

func GetReminder(id string) (*pb.Reminder, error) {
	return nil, nil
}

func ListReminders(
	limit *int64,
	offset *int64,
	message *string,
	userID *int64,
	periodStart *time.Time,
	periodEnd *time.Time,
) ([]*pb.Reminder, error) {
	return nil, nil
}

func ExpiredReminders(now *time.Time) ([]*pb.Reminder, error) {
	return nil, nil
}
