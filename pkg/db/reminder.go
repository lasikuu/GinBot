package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// staleClaimGrace bounds SENT before a reclaim; dispatch is serial, so a large
// backlog can exceed it and double-post.
const staleClaimGrace = 2 * time.Minute

// maxDeliveryAttempts stops a permanently rejected confirmation re-posting forever.
const maxDeliveryAttempts = 5

type ClaimedReminder struct {
	ID             string
	Message        *string
	PlatformEnum   int32
	DestinationUID *string
	// OwnerPlatformUID is nil when the owner is unlinked on that platform.
	OwnerPlatformUID *string
}

var ErrReminderCapReached = errors.New("reminder cap reached")

// CreateReminder inserts a reminder and returns its UUIDv7 and its
// per-user-allocated ref, refusing with ErrReminderCapReached at maxActive.
// The cap and the ref allocation share one per-owner advisory lock: READ
// COMMITTED hides a concurrent insert from both the count and the MAX(ref).
func CreateReminder(
	ctx context.Context,
	req *pb.CreateReminderReq,
	userID string,
	destinationID int64,
	maxActive int64,
) (id string, ref int64, err error) {
	reminderUUID, err := uuid.NewV7()
	if err != nil {
		return "", 0, fmt.Errorf("generate reminder uuid: %w", err)
	}
	reminderID := reminderUUID.String()

	// The column is `timestamp without time zone`; every stored instant is UTC.
	datetime := req.GetDatetime().AsTime().UTC()

	tx, err := db().Begin(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("begin reminder insert: %w", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			log.Z.Warn("failed to roll back reminder insert", zap.Error(err))
		}
	}()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID,
	); err != nil {
		return "", 0, fmt.Errorf("lock reminder owner: %w", err)
	}

	var active int64
	if err := tx.QueryRow(ctx, activeReminderCountSQL,
		userID, int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
	).Scan(&active); err != nil {
		return "", 0, fmt.Errorf("count active reminders: %w", err)
	}
	if active >= maxActive {
		return "", 0, ErrReminderCapReached
	}

	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(ref), 0) + 1 FROM reminder WHERE user_id = $1`, userID,
	).Scan(&ref); err != nil {
		return "", 0, fmt.Errorf("allocate reminder ref: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO reminder
		     (id, ref, datetime, timezone, repeat_cron, destination_id, message, user_id, parent_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		reminderID,
		ref,
		datetime,
		req.GetTimezone(),
		nullStr(req.GetRepeatCron()),
		destinationID,
		nullStr(req.GetMessage()),
		userID,
		nullStr(req.GetParentId()),
	); err != nil {
		return "", 0, fmt.Errorf("insert reminder: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", 0, fmt.Errorf("commit reminder insert: %w", err)
	}

	return reminderID, ref, nil
}

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

// GetReminderByRef resolves the alias described in ADR-0039, scoped to userID
// since a reminder's ref is only unique per owner.
func GetReminderByRef(ctx context.Context, userID string, ref int64) (*model.Reminder, error) {
	var reminder model.Reminder
	err := db().QueryRow(ctx,
		`SELECT `+model.ReminderColumns+` FROM reminder WHERE user_id = $1 AND ref = $2 AND deleted = FALSE`,
		userID, ref,
	).Scan(reminder.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan reminder by ref: %w", err)
	}

	return &reminder, nil
}

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

// ClaimDueReminders atomically flips every due reminder PENDING -> SENT as it
// returns it, so the next tick cannot re-claim it and double-deliver.
func ClaimDueReminders(ctx context.Context, now time.Time) ([]ClaimedReminder, error) {
	rows, err := db().Query(ctx,
		`WITH due AS (
		     UPDATE reminder
		        SET status = $2,
		            claimed_at = $4,
		            delivery_attempts = delivery_attempts + 1
		      WHERE datetime <= $1
		        AND status = $3
		        AND deleted = FALSE
		  RETURNING id, message, destination_id, user_id
		 )
		 SELECT due.id,
		        due.message,
		        instance.platform_enum,
		        -- 'destination_uid' is the storage contract owned by
		        -- callermeta.FieldDestinationUID, which is what writes it. The
		        -- claim tests assert this extraction against fixtures built from
		        -- that constant, so a rename cannot drift silently.
		        destination.destination_meta->>'destination_uid' AS destination_uid,
		        platform_user.platform_uid AS owner_platform_uid
		   FROM due
		   JOIN destination ON due.destination_id = destination.id
		   JOIN instance    ON destination.instance_id = instance.id
		   LEFT JOIN platform_user
		          ON platform_user.user_id = due.user_id
		         AND platform_user.platform_enum = instance.platform_enum
		         AND platform_user.deleted = FALSE`,
		now.UTC(),
		int32(pb.ReminderStatus_REMINDER_STATUS_SENT.Number()),
		int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
		now.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("claim due reminders: %w", err)
	}
	defer rows.Close()

	var claimed []ClaimedReminder
	for rows.Next() {
		var c ClaimedReminder
		if err := rows.Scan(&c.ID, &c.Message, &c.PlatformEnum, &c.DestinationUID, &c.OwnerPlatformUID); err != nil {
			return nil, fmt.Errorf("scan claimed reminder: %w", err)
		}
		claimed = append(claimed, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed reminders: %w", err)
	}

	return claimed, nil
}

type ReclaimOutcome struct {
	Retried int64
	// FailedOut had already spent maxDeliveryAttempts and were moved to FAILED.
	FailedOut int64
}

// ReclaimStaleReminders returns reminders stuck in SENT to PENDING, or to FAILED
// once maxDeliveryAttempts is spent. Staleness is measured from claimed_at, not
// updated_at, whose session-timezone-dependent cast makes the comparison wrong.
func ReclaimStaleReminders(ctx context.Context, now time.Time) (ReclaimOutcome, error) {
	cutoff := now.UTC().Add(-staleClaimGrace)

	// One statement so a row cannot land in both buckets. The CASE arms need
	// explicit casts: Postgres defaults a parameter inside a CASE to text.
	rows, err := db().Query(ctx,
		`UPDATE reminder
		    SET status = CASE WHEN delivery_attempts >= $1::int THEN $2::int ELSE $3::int END,
		        claimed_at = NULL
		  WHERE status = $4
		    AND deleted = FALSE
		    AND claimed_at <= $5
		RETURNING status`,
		int32(maxDeliveryAttempts),
		int32(pb.ReminderStatus_REMINDER_STATUS_FAILED.Number()),
		int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
		int32(pb.ReminderStatus_REMINDER_STATUS_SENT.Number()),
		cutoff,
	)
	if err != nil {
		return ReclaimOutcome{}, fmt.Errorf("reclaim stale reminders: %w", err)
	}
	defer rows.Close()

	var outcome ReclaimOutcome
	for rows.Next() {
		var newStatus int32
		if err := rows.Scan(&newStatus); err != nil {
			return ReclaimOutcome{}, fmt.Errorf("scan reclaimed reminder: %w", err)
		}
		if newStatus == int32(pb.ReminderStatus_REMINDER_STATUS_FAILED.Number()) {
			outcome.FailedOut++
			continue
		}
		outcome.Retried++
	}
	if err := rows.Err(); err != nil {
		return ReclaimOutcome{}, fmt.Errorf("iterate reclaimed reminders: %w", err)
	}

	return outcome, nil
}

// AdvanceReminderStatusIfSent moves a reminder off SENT and reports whether a
// row changed, so a duplicate ConfirmDelivery is a no-op.
func AdvanceReminderStatusIfSent(ctx context.Context, id string, statusEnum pb.ReminderStatus) (bool, error) {
	tag, err := db().Exec(ctx,
		`UPDATE reminder
		    SET status = $1,
		        claimed_at = NULL
		  WHERE id = $2
		    AND status = $3
		    AND deleted = FALSE`,
		int32(statusEnum.Number()), id,
		int32(pb.ReminderStatus_REMINDER_STATUS_SENT.Number()),
	)
	if err != nil {
		return false, fmt.Errorf("advance reminder status: %w", err)
	}

	return tag.RowsAffected() > 0, nil
}

func RescheduleReminderIfSent(ctx context.Context, id string, next time.Time) (bool, error) {
	tag, err := db().Exec(ctx,
		`UPDATE reminder
		    SET datetime = $1,
		        status = $2,
		        claimed_at = NULL,
		        delivery_attempts = 0
		  WHERE id = $3
		    AND status = $4
		    AND deleted = FALSE`,
		next.UTC(),
		int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
		id,
		int32(pb.ReminderStatus_REMINDER_STATUS_SENT.Number()),
	)
	if err != nil {
		return false, fmt.Errorf("reschedule reminder: %w", err)
	}

	return tag.RowsAffected() > 0, nil
}

const activeReminderCountSQL = `SELECT COUNT(*)
		   FROM reminder
		  WHERE user_id = $1
		    AND status = $2
		    AND deleted = FALSE`

func CountActiveReminders(ctx context.Context, userID string) (int64, error) {
	var count int64
	err := db().QueryRow(ctx, activeReminderCountSQL,
		userID, int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active reminders: %w", err)
	}

	return count, nil
}

// ListRemindersFilter narrows a reminder listing. UserID is mandatory and set by
// the caller, never the request, so a user can only list their own reminders.
type ListRemindersFilter struct {
	UserID        string
	Limit         int64
	Offset        int64
	MessageSearch string
	Status        *int32
	PeriodStart   *time.Time
	PeriodEnd     *time.Time
}

const defaultReminderListLimit = 50

type ListedReminder struct {
	Reminder    *model.Reminder
	Destination *pb.ReminderDestination
}

func ListRemindersByUser(ctx context.Context, filter ListRemindersFilter) ([]ListedReminder, error) {
	// Columns are qualified because `deleted` and the timestamps exist on all
	// three joined tables.
	args := []any{filter.UserID}
	var conditions []string
	conditions = append(conditions, "reminder.user_id = $1", "reminder.deleted = FALSE")

	if filter.Status != nil {
		args = append(args, *filter.Status)
		conditions = append(conditions, fmt.Sprintf("reminder.status = $%d", len(args)))
	}
	if filter.MessageSearch != "" {
		args = append(args, "%"+filter.MessageSearch+"%")
		conditions = append(conditions, fmt.Sprintf("reminder.message ILIKE $%d", len(args)))
	}
	if filter.PeriodStart != nil {
		args = append(args, filter.PeriodStart.UTC())
		conditions = append(conditions, fmt.Sprintf("reminder.datetime >= $%d", len(args)))
	}
	if filter.PeriodEnd != nil {
		args = append(args, filter.PeriodEnd.UTC())
		conditions = append(conditions, fmt.Sprintf("reminder.datetime <= $%d", len(args)))
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultReminderListLimit
	}
	args = append(args, limit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))
	args = append(args, filter.Offset)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args))

	query := `SELECT ` + prefixed(model.ReminderColumns, "reminder") + `,
		        instance.platform_enum,
		        instance.instance_meta,
		        destination.destination_meta
		 FROM reminder
		 JOIN destination ON reminder.destination_id = destination.id
		 JOIN instance    ON destination.instance_id = instance.id
		 WHERE ` + strings.Join(conditions, " AND ") + `
		 ORDER BY reminder.datetime ASC
		 LIMIT ` + limitPlaceholder + ` OFFSET ` + offsetPlaceholder

	rows, err := db().Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query reminders by user: %w", err)
	}
	defer rows.Close()

	var listed []ListedReminder
	for rows.Next() {
		var reminder model.Reminder
		var platformEnum int32
		var instanceMeta, destinationMeta *structpb.Struct

		targets := append(reminder.ScanTargets(), &platformEnum, &instanceMeta, &destinationMeta)
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("scan listed reminder: %w", err)
		}

		platform := pb.Platform(platformEnum)
		listed = append(listed, ListedReminder{
			Reminder: &reminder,
			Destination: pb.ReminderDestination_builder{
				PlatformEnum:    &platform,
				InstanceMeta:    instanceMeta,
				DestinationMeta: destinationMeta,
			}.Build(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listed reminders: %w", err)
	}

	return listed, nil
}

// SoftDeleteReminderByUser marks a caller-owned reminder deleted, reporting
// ErrNotFound for another user's reminder so it cannot be probed.
func SoftDeleteReminderByUser(ctx context.Context, id string, userID string) error {
	tag, err := db().Exec(ctx,
		`UPDATE reminder
		    SET deleted = TRUE
		  WHERE id = $1
		    AND user_id = $2
		    AND deleted = FALSE`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("soft delete reminder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

// ReminderUpdate is a full replace of every field except the patch-shaped
// message and repeat schedule.
type ReminderUpdate struct {
	ID            string
	UserID        string
	Datetime      time.Time
	Timezone      string
	DestinationID int64

	// UpdateMessage says whether Message is written at all: false leaves the
	// stored text untouched, true writes it, and an empty Message clears it.
	UpdateMessage bool
	Message       string

	// UpdateRepeat says whether RepeatCron is written at all: false leaves the
	// stored schedule untouched, true writes it, and an empty RepeatCron clears it.
	UpdateRepeat bool
	RepeatCron   string
}

// UpdateReminderByUser applies a caller-owned edit, reporting ErrNotFound when
// it is not the caller's. It re-arms status to PENDING so the claim sees it.
func UpdateReminderByUser(ctx context.Context, update ReminderUpdate) error {
	tag, err := db().Exec(ctx,
		`UPDATE reminder
		    SET datetime = $1,
		        timezone = $2,
		        message = CASE WHEN $3::boolean THEN $4::text ELSE message END,
		        destination_id = $5,
		        repeat_cron = CASE WHEN $6::boolean THEN $7::text ELSE repeat_cron END,
		        status = $8,
		        claimed_at = NULL,
		        delivery_attempts = 0
		  WHERE id = $9
		    AND user_id = $10
		    AND deleted = FALSE`,
		update.Datetime.UTC(), update.Timezone,
		update.UpdateMessage, nullStr(update.Message), update.DestinationID,
		update.UpdateRepeat, nullStr(update.RepeatCron),
		int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
		update.ID, update.UserID,
	)
	if err != nil {
		return fmt.Errorf("update reminder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}
