package db

import (
	"context"
	"fmt"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// CreateActionRecord stores an empty actorID as NULL. actionTime is the
// milliseconds a bounded action took, or nil when it does not apply.
func CreateActionRecord(ctx context.Context, actionType pb.ActionType, actorID string, actionTime *int64) error {
	_, err := db().Exec(ctx,
		`INSERT INTO action_record (action_type, actor_id, action_time)
		 VALUES ($1, $2, $3)`,
		int32(actionType.Number()), nullStr(actorID), actionTime,
	)
	if err != nil {
		return fmt.Errorf("insert action record: %w", err)
	}

	return nil
}
