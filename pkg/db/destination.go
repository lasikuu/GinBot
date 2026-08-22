package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// GetOrCreateDestinationByMeta resolves a platform/instance/destination triple to
// a destination id, creating the instance and destination rows if they do not
// exist yet. This is how a guild or room first becomes known to the bot.
// It runs in a transaction so the instance and destination are resolved
// atomically, and both inserts use ON CONFLICT so concurrent callers converge on
// the same rows rather than creating duplicates.
func GetOrCreateDestinationByMeta(ctx context.Context, destination *pb.ReminderDestination) (int64, error) {
	platformEnum := destination.GetPlatformEnum()
	instanceMeta := destination.GetInstanceMeta()
	destinationMeta := destination.GetDestinationMeta()

	tx, err := db().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			log.Z.Warn("failed to roll back destination resolution", zap.Error(err))
		}
	}()

	// DO UPDATE rather than DO NOTHING: DO NOTHING returns no row on conflict, so
	// RETURNING would yield nothing for an instance that already exists.
	var instanceID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO instance (platform_enum, instance_meta)
		 VALUES ($1, $2)
		 ON CONFLICT (platform_enum, instance_meta)
		     DO UPDATE SET platform_enum = instance.platform_enum
		 RETURNING id`,
		platformEnum.Number(), instanceMeta,
	).Scan(&instanceID); err != nil {
		return 0, fmt.Errorf("get or create instance: %w", err)
	}

	var destinationID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO destination (instance_id, destination_meta)
		 VALUES ($1, $2)
		 ON CONFLICT (instance_id, destination_meta)
		     DO UPDATE SET instance_id = destination.instance_id
		 RETURNING id`,
		instanceID, destinationMeta,
	).Scan(&destinationID); err != nil {
		return 0, fmt.Errorf("get or create destination: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit destination resolution: %w", err)
	}

	return destinationID, nil
}

// CreateDestination inserts a destination under an instance and returns its id.
func CreateDestination(ctx context.Context, instanceID int64, destinationMeta *structpb.Struct) (int64, error) {
	var destinationID int64
	err := db().QueryRow(ctx,
		`INSERT INTO destination (instance_id, destination_meta) VALUES ($1, $2) RETURNING id`,
		instanceID, destinationMeta,
	).Scan(&destinationID)
	if err != nil {
		return 0, fmt.Errorf("insert destination: %w", err)
	}

	return destinationID, nil
}

// GetReminderDestination rebuilds the protobuf destination for a destination id
// by joining back to its instance.
func GetReminderDestination(ctx context.Context, destinationID int64) (*pb.ReminderDestination, error) {
	var platformEnum int32
	var instanceMeta *structpb.Struct
	var destinationMeta *structpb.Struct

	err := db().QueryRow(ctx,
		`SELECT instance.platform_enum, instance.instance_meta, destination.destination_meta
		 FROM destination
		 JOIN instance ON destination.instance_id = instance.id
		 WHERE destination.id = $1`,
		destinationID,
	).Scan(&platformEnum, &instanceMeta, &destinationMeta)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan reminder destination: %w", err)
	}

	platform := pb.Platform(platformEnum)
	return pb.ReminderDestination_builder{
		PlatformEnum:    &platform,
		InstanceMeta:    instanceMeta,
		DestinationMeta: destinationMeta,
	}.Build(), nil
}
