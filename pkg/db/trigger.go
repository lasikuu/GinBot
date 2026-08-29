package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/trigger"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// exactPhraseConstraint is the partial unique index enforcing global,
// case-insensitive uniqueness of exact-mode phrases. Named so a violation is
// identified by constraint rather than by matching the error message.
const exactPhraseConstraint = "uq_trigger_exact_phrase"

// ErrExactPhraseTaken is returned when an exact-mode phrase already exists.
var ErrExactPhraseTaken = errors.New("exact phrase already exists")

func isExactPhraseViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == exactPhraseConstraint
}

type CreateTriggerParams struct {
	Phrase string
	Reply  string
	FileID string
	UserID string
	Chance int32
	Mode   pb.TriggerMode
	// InstanceIDs must hold at least one: a trigger scoped to nothing never fires.
	InstanceIDs []int64
}

// CreateTrigger writes the trigger and its instance scoping in one transaction.
func CreateTrigger(ctx context.Context, params CreateTriggerParams) (string, error) {
	if len(params.InstanceIDs) == 0 {
		return "", fmt.Errorf("create trigger: at least one instance is required")
	}

	triggerUUID, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate trigger uuid: %w", err)
	}
	triggerID := triggerUUID.String()

	tx, err := db().Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin trigger insert: %w", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			log.Z.Warn("failed to roll back trigger insert", zap.Error(err))
		}
	}()

	if _, err := tx.Exec(ctx,
		`INSERT INTO trigger (id, phrase, reply, file_id, user_id, chance, mode)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		triggerID, params.Phrase, nullStr(params.Reply), nullStr(params.FileID), nullStr(params.UserID),
		params.Chance, int32(params.Mode.Number()),
	); err != nil {
		if isExactPhraseViolation(err) {
			return "", ErrExactPhraseTaken
		}
		return "", fmt.Errorf("insert trigger: %w", err)
	}

	// The caller's instance list is not deduplicated upstream, so a repeated pair
	// must not fail the create.
	for _, instanceID := range params.InstanceIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO trigger_instance (trigger_id, instance_id)
			 VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`,
			triggerID, instanceID,
		); err != nil {
			return "", fmt.Errorf("insert trigger instance: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit trigger insert: %w", err)
	}

	return triggerID, nil
}

// GetTrigger returns one trigger by id, or ErrNotFound. Soft-deleted rows are
// not returned.
func GetTrigger(ctx context.Context, id string) (*model.Trigger, error) {
	var row model.Trigger
	err := db().QueryRow(ctx,
		`SELECT `+model.TriggerColumns+` FROM trigger WHERE id = $1 AND deleted = FALSE`,
		id,
	).Scan(row.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan trigger: %w", err)
	}

	return &row, nil
}

// GetTriggerInstances rebuilds the protobuf shape here because internal/model
// does not know about pb.TriggerInstance.
func GetTriggerInstances(ctx context.Context, triggerID string) ([]*pb.TriggerInstance, error) {
	rows, err := db().Query(ctx,
		`SELECT instance.platform_enum, instance.instance_meta
		 FROM trigger_instance
		 JOIN instance ON trigger_instance.instance_id = instance.id
		 WHERE trigger_instance.trigger_id = $1
		   AND instance.deleted = FALSE`,
		triggerID,
	)
	if err != nil {
		return nil, fmt.Errorf("query trigger instances: %w", err)
	}
	defer rows.Close()

	var instances []*pb.TriggerInstance
	for rows.Next() {
		var platformEnum int32
		var instanceMeta *structpb.Struct
		if err := rows.Scan(&platformEnum, &instanceMeta); err != nil {
			return nil, fmt.Errorf("scan trigger instance: %w", err)
		}

		platform := pb.Platform(platformEnum)
		instances = append(instances, pb.TriggerInstance_builder{
			PlatformEnum: &platform,
			InstanceMeta: instanceMeta,
		}.Build())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trigger instances: %w", err)
	}

	return instances, nil
}

// ListTriggerInstanceIDs returns raw ids for Cache.Invalidate and the handler
// origin checks; GetTriggerInstances returns the protobuf shape for responses.
func ListTriggerInstanceIDs(ctx context.Context, triggerID string) ([]int64, error) {
	rows, err := db().Query(ctx,
		`SELECT instance_id FROM trigger_instance WHERE trigger_id = $1`,
		triggerID,
	)
	if err != nil {
		return nil, fmt.Errorf("query trigger instance ids: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan trigger instance id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trigger instance ids: %w", err)
	}

	return ids, nil
}

type ListTriggersFilter struct {
	// UserID scopes to one creator when non-empty.
	UserID string
	// InstanceID scopes to one instance when non-zero.
	InstanceID   int64
	PhraseSearch string
	ReplySearch  string
	Mode         *int32
	PeriodStart  *time.Time
	PeriodEnd    *time.Time
	Limit        int64
	Offset       int64
}

const (
	defaultTriggerListLimit = 50
	maxTriggerListLimit     = 200
)

// ListTriggers returns triggers matching a filter, newest first.
func ListTriggers(ctx context.Context, filter ListTriggersFilter) ([]*model.Trigger, error) {
	var args []any
	conditions := []string{"deleted = FALSE"}

	if filter.UserID != "" {
		args = append(args, filter.UserID)
		conditions = append(conditions, fmt.Sprintf("user_id = $%d", len(args)))
	}
	if filter.InstanceID != 0 {
		args = append(args, filter.InstanceID)
		conditions = append(conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM trigger_instance WHERE trigger_instance.trigger_id = trigger.id AND trigger_instance.instance_id = $%d)",
			len(args)))
	}
	if filter.PhraseSearch != "" {
		args = append(args, "%"+filter.PhraseSearch+"%")
		conditions = append(conditions, fmt.Sprintf("phrase ILIKE $%d", len(args)))
	}
	if filter.ReplySearch != "" {
		args = append(args, "%"+filter.ReplySearch+"%")
		conditions = append(conditions, fmt.Sprintf("reply ILIKE $%d", len(args)))
	}
	if filter.Mode != nil {
		args = append(args, *filter.Mode)
		conditions = append(conditions, fmt.Sprintf("mode = $%d", len(args)))
	}
	if filter.PeriodStart != nil {
		args = append(args, filter.PeriodStart.UTC())
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	if filter.PeriodEnd != nil {
		args = append(args, filter.PeriodEnd.UTC())
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", len(args)))
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultTriggerListLimit
	}
	if limit > maxTriggerListLimit {
		limit = maxTriggerListLimit
	}
	args = append(args, limit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))
	// Postgres rejects a negative OFFSET as an opaque Internal error.
	offset := max(filter.Offset, 0)
	args = append(args, offset)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args))

	// id breaks ties: created_at is microsecond precision and rows from one
	// transaction share it, which makes OFFSET paging drop or repeat rows.
	query := `SELECT ` + model.TriggerColumns + `
		 FROM trigger
		 WHERE ` + strings.Join(conditions, " AND ") + `
		 ORDER BY created_at DESC, id DESC
		 LIMIT ` + limitPlaceholder + ` OFFSET ` + offsetPlaceholder

	rows, err := db().Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query triggers: %w", err)
	}
	defer rows.Close()

	var triggers []*model.Trigger
	for rows.Next() {
		var row model.Trigger
		if err := rows.Scan(row.ScanTargets()...); err != nil {
			return nil, fmt.Errorf("scan listed trigger: %w", err)
		}
		triggers = append(triggers, &row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate triggers: %w", err)
	}

	return triggers, nil
}

type TriggerUpdate struct {
	ID     string
	UserID string
	// Each Update* flag lets its paired field write an empty value deliberately.
	UpdatePhrase bool
	Phrase       string
	UpdateReply  bool
	Reply        string
	UpdateFile   bool
	FileID       string
	UpdateChance bool
	Chance       int32
	UpdateMode   bool
	Mode         pb.TriggerMode
}

// UpdateTriggerByUser returns ErrNotFound both when the trigger is missing and
// when it belongs to someone else, so a caller cannot probe another's triggers.
func UpdateTriggerByUser(ctx context.Context, update TriggerUpdate) error {
	tag, err := db().Exec(ctx,
		`UPDATE trigger
		    SET phrase  = CASE WHEN $1::boolean THEN $2::text ELSE phrase  END,
		        reply   = CASE WHEN $3::boolean THEN $4::text ELSE reply   END,
		        file_id = CASE WHEN $5::boolean THEN $6::uuid ELSE file_id END,
		        chance  = CASE WHEN $7::boolean THEN $8::int  ELSE chance  END,
		        mode    = CASE WHEN $9::boolean THEN $10::int ELSE mode    END
		  WHERE id = $11
		    AND user_id = $12
		    AND deleted = FALSE`,
		update.UpdatePhrase, update.Phrase,
		update.UpdateReply, nullStr(update.Reply),
		update.UpdateFile, nullStr(update.FileID),
		update.UpdateChance, update.Chance,
		update.UpdateMode, int32(update.Mode.Number()),
		update.ID, update.UserID,
	)
	if err != nil {
		if isExactPhraseViolation(err) {
			return ErrExactPhraseTaken
		}
		return fmt.Errorf("update trigger: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

// SoftDeleteTriggerByUser returns ErrNotFound for a trigger that is not theirs.
func SoftDeleteTriggerByUser(ctx context.Context, id string, userID string) error {
	tag, err := db().Exec(ctx,
		`UPDATE trigger
		    SET deleted = TRUE
		  WHERE id = $1
		    AND user_id = $2
		    AND deleted = FALSE`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("soft delete trigger: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

type ActiveTrigger struct {
	ID     string
	Phrase string
	Chance int32
	Mode   int32
}

// ListActiveTriggersByInstance backs the in-memory compiled set, so it runs on a
// cache miss and never per message.
func ListActiveTriggersByInstance(ctx context.Context, instanceID int64) ([]ActiveTrigger, error) {
	rows, err := db().Query(ctx,
		`SELECT trigger.id, trigger.phrase, trigger.chance, trigger.mode
		 FROM trigger
		 JOIN trigger_instance ON trigger_instance.trigger_id = trigger.id
		 WHERE trigger_instance.instance_id = $1
		   AND trigger.deleted = FALSE
		 ORDER BY trigger.id
		 LIMIT $2`,
		instanceID, trigger.MaxCandidates,
	)
	if err != nil {
		return nil, fmt.Errorf("query active triggers: %w", err)
	}
	defer rows.Close()

	var actives []ActiveTrigger
	for rows.Next() {
		var active ActiveTrigger
		if err := rows.Scan(&active.ID, &active.Phrase, &active.Chance, &active.Mode); err != nil {
			return nil, fmt.Errorf("scan active trigger: %w", err)
		}
		actives = append(actives, active)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active triggers: %w", err)
	}

	return actives, nil
}

type TriggerStatRow struct {
	TriggerID string
	Phrase    string
	Count     int64
	Chance    int32
	Mode      int32
}

type TriggerStatsFilter struct {
	InstanceID  int64
	ActionType  pb.ActionType
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	Limit       int64
}

const (
	defaultTriggerStatsLimit = 10
	maxTriggerStatsLimit     = 100
)

// ListTriggerStats aggregates action_record rather than a counter column, since
// only an event log can be queried by period.
func ListTriggerStats(ctx context.Context, filter TriggerStatsFilter) ([]TriggerStatRow, error) {
	args := []any{filter.InstanceID, int32(filter.ActionType.Number())}
	conditions := []string{
		"trigger_instance.instance_id = $1",
		"action_record.action_type = $2",
		"trigger.deleted = FALSE",
	}

	if filter.PeriodStart != nil {
		args = append(args, filter.PeriodStart.UTC())
		conditions = append(conditions, fmt.Sprintf("action_record.action_timestamp >= $%d", len(args)))
	}
	if filter.PeriodEnd != nil {
		args = append(args, filter.PeriodEnd.UTC())
		conditions = append(conditions, fmt.Sprintf("action_record.action_timestamp <= $%d", len(args)))
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultTriggerStatsLimit
	}
	if limit > maxTriggerStatsLimit {
		limit = maxTriggerStatsLimit
	}
	args = append(args, limit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))

	query := `SELECT trigger.id, trigger.phrase, COUNT(action_record.id), trigger.chance, trigger.mode
		 FROM action_record
		 JOIN trigger ON trigger.id = action_record.subject_id
		 JOIN trigger_instance ON trigger_instance.trigger_id = trigger.id
		 WHERE ` + strings.Join(conditions, " AND ") + `
		 GROUP BY trigger.id, trigger.phrase, trigger.chance, trigger.mode
		 ORDER BY COUNT(action_record.id) DESC, trigger.id ASC
		 LIMIT ` + limitPlaceholder

	rows, err := db().Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query trigger stats: %w", err)
	}
	defer rows.Close()

	var stats []TriggerStatRow
	for rows.Next() {
		var row TriggerStatRow
		if err := rows.Scan(&row.TriggerID, &row.Phrase, &row.Count, &row.Chance, &row.Mode); err != nil {
			return nil, fmt.Errorf("scan trigger stat: %w", err)
		}
		stats = append(stats, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trigger stats: %w", err)
	}

	return stats, nil
}

// RecordTriggerFire takes ACTION_TYPE_TRIGGER_OCCURRED for a chance fire and
// ACTION_TYPE_TRIGGER_CALLED for a forced one or an explicit execution.
func RecordTriggerFire(ctx context.Context, actionType pb.ActionType, triggerID string, actorID string) error {
	_, err := db().Exec(ctx,
		`INSERT INTO action_record (action_type, actor_id, subject_id)
		 VALUES ($1, $2, $3)`,
		int32(actionType.Number()), nullStr(actorID), nullStr(triggerID),
	)
	if err != nil {
		return fmt.Errorf("insert trigger fire record: %w", err)
	}

	return nil
}
