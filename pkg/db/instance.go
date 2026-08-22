package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// CreateInstance inserts an instance and returns its id.
func CreateInstance(
	ctx context.Context,
	platformEnum pb.Platform,
	instanceMeta *structpb.Struct,
	defaultChannel string,
) (int64, error) {
	var instanceID int64
	err := db().QueryRow(ctx,
		`INSERT INTO instance (platform_enum, instance_meta, default_channel)
		 VALUES ($1, $2, $3) RETURNING id`,
		platformEnum.Number(), instanceMeta, nullStr(defaultChannel),
	).Scan(&instanceID)
	if err != nil {
		return 0, fmt.Errorf("insert instance: %w", err)
	}

	return instanceID, nil
}

// GetInstanceByID returns the instance row for id, or ErrNotFound.
func GetInstanceByID(ctx context.Context, id int64) (*model.Instance, error) {
	var instance model.Instance
	err := db().QueryRow(ctx,
		`SELECT `+model.InstanceColumns+` FROM instance WHERE id = $1 AND deleted = FALSE`,
		id,
	).Scan(instance.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan instance: %w", err)
	}

	return &instance, nil
}

// GetInstanceByMeta looks an instance up by its platform and metadata, or ErrNotFound.
func GetInstanceByMeta(
	ctx context.Context,
	platformEnum pb.Platform,
	instanceMeta *structpb.Struct,
) (*model.Instance, error) {
	var instance model.Instance
	err := db().QueryRow(ctx,
		`SELECT `+model.InstanceColumns+`
		 FROM instance
		 WHERE platform_enum = $1 AND instance_meta = $2 AND deleted = FALSE`,
		platformEnum.Number(), instanceMeta,
	).Scan(instance.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan instance by meta: %w", err)
	}

	return &instance, nil
}

// GetOrCreateInstanceByMeta returns the id of the matching instance, inserting
// one if it does not exist yet.
//
// A plain read-then-insert would race: two concurrent callers for the same guild
// both miss the SELECT and both insert. ON CONFLICT against
// uq_instance_platform_meta makes the insert idempotent, and the DO UPDATE (a
// no-op write of the same value) is what allows RETURNING to yield the existing
// row's id on conflict — DO NOTHING returns no row at all.
func GetOrCreateInstanceByMeta(
	ctx context.Context,
	platformEnum pb.Platform,
	instanceMeta *structpb.Struct,
) (int64, error) {
	var instanceID int64
	err := db().QueryRow(ctx,
		`INSERT INTO instance (platform_enum, instance_meta)
		 VALUES ($1, $2)
		 ON CONFLICT (platform_enum, instance_meta)
		     DO UPDATE SET platform_enum = instance.platform_enum
		 RETURNING id`,
		platformEnum.Number(), instanceMeta,
	).Scan(&instanceID)
	if err != nil {
		return 0, fmt.Errorf("get or create instance: %w", err)
	}

	return instanceID, nil
}
