package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

// CreateReminder inserts a reminder and returns its UUIDv7.
func CreateReminder(ctx context.Context, req *pb.CreateReminderReq, userID string, destinationID int64) (string, error) {
	reminderUUID, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate reminder uuid: %w", err)
	}
	reminderID := reminderUUID.String()

	// The timestamp column is `timestamp without time zone` and every stored
	// instant is UTC; the caller's zone is kept separately in `timezone`.
	datetime := req.GetDatetime().AsTime().UTC()

	_, err = db().Exec(ctx,
		`INSERT INTO reminder
		     (id, datetime, timezone, repeat_cron, destination_id, message, user_id, parent_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		reminderID,
		datetime,
		req.GetTimezone(),
		nullStr(req.GetRepeatCron()),
		destinationID,
		nullStr(req.GetMessage()),
		userID,
		nullStr(req.GetParentId()),
	)
	if err != nil {
		return "", fmt.Errorf("insert reminder: %w", err)
	}

	return reminderID, nil
}

// GetReminder returns the reminder row for id, or ErrNotFound.
func GetReminder(ctx context.Context, id string) (*model.Reminder, error) {
	var reminder model.Reminder
	err := db().QueryRow(ctx,
		`SELECT `+model.ReminderColumns+` FROM reminder WHERE id = $1 AND deleted = FALSE`,
		id,
	).Scan(reminder.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan reminder: %w", err)
	}

	return &reminder, nil
}

// ExpiredReminders returns pending reminders whose fire time has passed.
func ExpiredReminders(ctx context.Context, now time.Time) ([]*model.Reminder, error) {
	rows, err := db().Query(ctx,
		`SELECT `+model.ReminderColumns+`
		 FROM reminder
		 WHERE datetime <= $1
		   AND status = $2
		   AND deleted = FALSE
		 ORDER BY datetime ASC`,
		now.UTC(), int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
	)
	if err != nil {
		return nil, fmt.Errorf("query expired reminders: %w", err)
	}
	defer rows.Close()

	var reminders []*model.Reminder
	for rows.Next() {
		var reminder model.Reminder
		if err := rows.Scan(reminder.ScanTargets()...); err != nil {
			return nil, fmt.Errorf("scan expired reminder: %w", err)
		}
		reminders = append(reminders, &reminder)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired reminders: %w", err)
	}

	return reminders, nil
}

// SetReminderStatus updates a reminder's delivery status.
func SetReminderStatus(ctx context.Context, id string, statusEnum pb.ReminderStatus) error {
	_, err := db().Exec(ctx,
		`UPDATE reminder SET status = $1 WHERE id = $2`,
		int32(statusEnum.Number()), id,
	)
	if err != nil {
		return fmt.Errorf("update reminder status: %w", err)
	}

	return nil
}
