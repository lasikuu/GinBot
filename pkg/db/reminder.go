package db

import (
	"time"

	"github.com/lasikuu/GinBot/pkg/gen/proto"
)

func GetReminder(id *int64, userID *int64) (*proto.Reminder, error) {
	return nil, nil
}

func ListReminders(
	limit *int64,
	offset *int64,
	message *string,
	userID *int64,
	periodStart *time.Time,
	periodEnd *time.Time,
) ([]*proto.Reminder, error) {
	return nil, nil
}

func ExpiredReminders(now *time.Time) ([]*proto.Reminder, error) {
	return nil, nil
}
