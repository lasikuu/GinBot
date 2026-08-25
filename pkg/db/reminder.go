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

// staleClaimGrace is how long a reminder may sit in SENT (claimed by a tick but
// never confirmed by a client) before a later tick may reclaim it back to
// PENDING and retry.
//
// A push is fire-and-forget: ReverseServer.SendAction drops on a full client
// buffer and returns nothing, so a claimed row whose push was dropped — or whose
// client died before confirming — would otherwise be stuck at SENT forever and
// silently never deliver.
//
// Two minutes is a compromise, not a safe margin. ONE delivery in isolation is
// sub-second (a Discord send plus a gRPC round trip), but deliveries are not
// isolated: pkg/grpc/client.dispatch runs handlers INLINE on the receive loop,
// so a client drains its 64-slot buffer strictly serially, and Discord rate
// limits a channel at roughly 5 requests per 5 seconds. A backlog of a few dozen
// reminders for one channel therefore puts the tail past 120s, and those tail
// reminders are reclaimed while still queued — which double-posts them.
//
// KNOWN LIMITATION, accepted for now: the damage is bounded rather than
// eliminated. maxDeliveryAttempts caps how many times any one reminder can be
// re-pushed, so a backlog cannot spam a channel indefinitely. The real fix is to
// dispatch notifications on a worker pool instead of inline, which is out of
// scope here.
const staleClaimGrace = 2 * time.Minute

// maxDeliveryAttempts is how many times a reminder may be claimed for delivery
// before the reclaim gives up and marks it FAILED instead of retrying.
//
// Without a cap, a confirmation that is rejected PERMANENTLY turns a *successful*
// post into an endless re-post loop: if the owner has no undeleted platform_user
// row for the platform, the client's outgoing ConfirmDelivery carries no user_id
// and the clearance interceptor answers InvalidArgument every time; the same
// happens with PermissionDenied if the owner's clearance drops below REGISTERED.
// The channel post succeeds on every cycle, so the user is notified once per
// grace period forever and the reminder never reaches a terminal status.
//
// Five is deliberately small: a genuinely transient fault (a client restart, a
// full buffer) resolves within one or two grace periods, so five attempts spans
// ten minutes of trying — long enough to ride out a deploy, short enough that a
// permanently broken reminder stops bothering the user quickly.
const maxDeliveryAttempts = 5

// ClaimedReminder is a reminder claimed for delivery together with everything
// the push needs, resolved in a single JOIN so the cron loop does not issue one
// destination query per reminder.
type ClaimedReminder struct {
	ID             string
	Message        *string
	PlatformEnum   int32
	DestinationUID *string
	// OwnerPlatformUID is the reminder owner's platform-scoped id (e.g. a Discord
	// snowflake) on the same platform as the destination, used for the DM
	// fallback and to build ConfirmDelivery caller metadata. Nil when the owner
	// has no platform_user row for that platform.
	OwnerPlatformUID *string
}

// ErrReminderCapReached reports that the owner already holds the maximum number
// of active reminders, so the insert was refused. The limit itself belongs to the
// service layer; this only signals that it was hit.
var ErrReminderCapReached = errors.New("reminder cap reached")

// CreateReminder inserts a reminder and returns its UUIDv7, refusing with
// ErrReminderCapReached when the owner is already at maxActive.
//
// The cap is enforced INSIDE the write, not by a separate count beforehand: a
// check-then-insert lets two concurrent creates at the limit both read the same
// count and both insert. The count and the insert therefore share one
// transaction, and that transaction first takes an advisory lock keyed on the
// owner so concurrent creates for the same user serialise. READ COMMITTED alone
// is not enough — each transaction's snapshot hides the other's uncommitted row,
// so both would still see the same count. The lock is per-owner, so creates by
// different users never contend, and it is released when the transaction ends.
func CreateReminder(
	ctx context.Context,
	req *pb.CreateReminderReq,
	userID string,
	destinationID int64,
	maxActive int64,
) (string, error) {
	reminderUUID, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate reminder uuid: %w", err)
	}
	reminderID := reminderUUID.String()

	// The timestamp column is `timestamp without time zone` and every stored
	// instant is UTC; the caller's zone is kept separately in `timezone`.
	datetime := req.GetDatetime().AsTime().UTC()

	tx, err := db().Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin reminder insert: %w", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			log.Z.Warn("failed to roll back reminder insert", zap.Error(err))
		}
	}()

	// hashtextextended maps the owner uuid onto the bigint an advisory lock takes.
	// A hash collision would only make two unrelated users' creates queue behind
	// each other for the length of one insert, never let a cap be exceeded.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID,
	); err != nil {
		return "", fmt.Errorf("lock reminder owner: %w", err)
	}

	var active int64
	if err := tx.QueryRow(ctx, activeReminderCountSQL,
		userID, int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
	).Scan(&active); err != nil {
		return "", fmt.Errorf("count active reminders: %w", err)
	}
	if active >= maxActive {
		return "", ErrReminderCapReached
	}

	if _, err := tx.Exec(ctx,
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
	); err != nil {
		return "", fmt.Errorf("insert reminder: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit reminder insert: %w", err)
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

// ClaimDueReminders atomically claims every reminder whose fire time has passed,
// flipping it PENDING -> SENT and returning it joined to its destination and
// owner in one query.
//
// The atomic status flip is what prevents double delivery: the row is SENT
// before this returns, so the next 1s tick's WHERE status = PENDING cannot
// re-claim it. The same UPDATE stamps claimed_at with the caller's `now` and
// increments delivery_attempts, which are the reclaim's clock and its give-up
// counter (see ReclaimStaleReminders). claimed_at is written explicitly rather
// than read back off updated_at, because updated_at is a
// `timestamp without time zone` produced by a session-timezone-dependent cast.
//
// The owner's platform id is resolved by a LEFT JOIN on platform_user for the
// destination's own platform, so a reminder whose owner never linked that
// platform still delivers to the channel (OwnerPlatformUID nil disables only the
// DM fallback).
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

// ReclaimOutcome reports what one reclaim pass did to the reminders it found
// stuck in SENT.
type ReclaimOutcome struct {
	// Retried were returned to PENDING for another delivery attempt.
	Retried int64
	// FailedOut had already used up maxDeliveryAttempts and were moved to FAILED
	// instead, so a reminder whose confirmation is rejected permanently stops
	// being re-posted.
	FailedOut int64
}

// ReclaimStaleReminders resolves reminders stuck in SENT: back to PENDING for
// another try, or to FAILED once the attempt cap is spent.
//
// A reminder is stuck when its push was dropped (full client buffer), its client
// died before calling ConfirmDelivery, or its confirmation is being rejected
// every time. Nothing else ever moves it off SENT.
//
// "Stuck" is measured from claimed_at, which ClaimDueReminders writes explicitly
// as an absolute instant. It is deliberately NOT measured from updated_at: that
// column is `timestamp without time zone` filled by a trigger as NOW(), so the
// timestamptz -> timestamp cast uses the session TimeZone, which pkg/db never
// pins — leaving the comparison against a Go-computed UTC cutoff silently wrong
// in one direction (never reclaim) or the other (reclaim everything instantly).
func ReclaimStaleReminders(ctx context.Context, now time.Time) (ReclaimOutcome, error) {
	cutoff := now.UTC().Add(-staleClaimGrace)

	// One statement rather than two, so a row cannot be counted in both buckets
	// or slip between them. claimed_at is cleared either way: a stale value must
	// never leak into a later delivery cycle.
	// The CASE arms are cast explicitly: without them Postgres has no context to
	// infer a parameter's type inside a CASE and defaults it to text, which fails
	// against the integer column.
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

// AdvanceReminderStatusIfSent moves a reminder off SENT, guarding against a
// double confirmation: only a reminder currently in SENT is advanced, so a
// retried or duplicate ConfirmDelivery for one already resolved is a no-op.
// It reports whether a row was advanced.
//
// claimed_at is cleared with the transition: the reminder is no longer in flight,
// and leaving the stamp behind would let a later reclaim measure age from a claim
// that has already been resolved.
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

// RescheduleReminderIfSent sets a repeating reminder's next fire time and returns
// it to PENDING, but only when it is currently SENT (same double-confirm guard
// as AdvanceReminderStatusIfSent). It reports whether a row was rescheduled.
//
// delivery_attempts is reset because this occurrence DID deliver: the next one
// starts a fresh cycle, and carrying the count forward would eventually fail out
// a perfectly healthy daily repeat.
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

// activeReminderCountSQL counts the reminders that occupy a user's per-user cap:
// their own, pending, not deleted. $1 is the owner, $2 the PENDING status.
//
// It is shared by CreateReminder (which enforces the cap inside its transaction)
// and CountActiveReminders (which the integration test asserts the predicate
// with), so the enforced rule and the tested rule cannot drift apart.
const activeReminderCountSQL = `SELECT COUNT(*)
		   FROM reminder
		  WHERE user_id = $1
		    AND status = $2
		    AND deleted = FALSE`

// CountActiveReminders returns the number of pending, non-deleted reminders a
// user owns. CreateReminder enforces the cap itself; this exposes the same
// predicate for diagnostics and for the test that pins it down.
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

// ListRemindersFilter narrows a caller-scoped reminder listing. A zero value
// applies no optional filter; UserID is mandatory and set by the caller, never
// the request, so a user can only ever list their own reminders.
type ListRemindersFilter struct {
	UserID        string
	Limit         int64
	Offset        int64
	MessageSearch string
	Status        *int32
	PeriodStart   *time.Time
	PeriodEnd     *time.Time
}

// defaultReminderListLimit bounds a listing that requests no limit, so a caller
// cannot pull an unbounded result set.
const defaultReminderListLimit = 50

// ListedReminder is a reminder together with its resolved destination.
//
// The destination is joined in rather than looked up per row: a listing returns
// up to defaultReminderListLimit reminders, and one GetReminderDestination call
// each turned a single command into 50 round trips.
type ListedReminder struct {
	Reminder    *model.Reminder
	Destination *pb.ReminderDestination
}

// ListRemindersByUser returns a caller's reminders with their destinations,
// SOONEST fire time first, honouring the optional filters.
//
// Ascending order matters with a limit: a user holding more reminders than the
// client renders needs to see the ones about to fire, not the ones furthest out.
//
// The destination and instance joins are inner joins, which cannot drop a row:
// reminder.destination_id and destination.instance_id are both NOT NULL with a
// foreign key behind them.
func ListRemindersByUser(ctx context.Context, filter ListRemindersFilter) ([]ListedReminder, error) {
	// $1 is always the owner; further filters are appended positionally so the
	// query is parameterised rather than interpolated. Every column is qualified
	// because `deleted`, `created_at` and `updated_at` exist on all three joined
	// tables and would otherwise be ambiguous.
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

// SoftDeleteReminderByUser marks a caller-owned reminder deleted. It reports
// ErrNotFound when no matching, owned, undeleted row exists — which also covers
// another user's reminder, so a caller cannot delete or probe one that is not
// theirs.
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

// ReminderUpdate is one caller-owned reminder edit.
//
// Every field is a full replace EXCEPT the repeat schedule, which is patched:
// see UpdateRepeat.
type ReminderUpdate struct {
	ID            string
	UserID        string
	Datetime      time.Time
	Timezone      string
	Message       string
	DestinationID int64

	// UpdateRepeat says whether RepeatCron should be written at all.
	//
	// It exists because a full replace destroyed data: /remindermod never sets a
	// repeat, so changing only a reminder's message rewrote repeat_cron to NULL
	// and silently turned a repeating reminder into a one-shot. False leaves the
	// stored schedule exactly as it was; true writes RepeatCron, and an empty
	// RepeatCron is the explicit "clear the repeat" sentinel. COALESCE cannot
	// express this — it has no way to distinguish "not supplied" from "cleared".
	UpdateRepeat bool
	RepeatCron   string
}

// UpdateReminderByUser applies a caller-owned reminder edit. It reports
// ErrNotFound when the reminder is not the caller's, matching GetReminder's
// privacy pattern.
//
// The edit RE-ARMS the reminder: status goes back to PENDING and the delivery
// bookkeeping is reset. Without that, moving an already DELIVERED or FAILED
// reminder to a future time cheerfully reported success and then never fired,
// because the claim only ever picks up PENDING rows. The handler refuses a
// non-future datetime, so this cannot re-arm something into the past.
func UpdateReminderByUser(ctx context.Context, update ReminderUpdate) error {
	tag, err := db().Exec(ctx,
		`UPDATE reminder
		    SET datetime = $1,
		        timezone = $2,
		        message = $3,
		        destination_id = $4,
		        repeat_cron = CASE WHEN $5::boolean THEN $6::text ELSE repeat_cron END,
		        status = $7,
		        claimed_at = NULL,
		        delivery_attempts = 0
		  WHERE id = $8
		    AND user_id = $9
		    AND deleted = FALSE`,
		update.Datetime.UTC(), update.Timezone, nullStr(update.Message), update.DestinationID,
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
