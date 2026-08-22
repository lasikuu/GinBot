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

// GetOrCreateDestinationByMeta resolves a platform/instance/destination triple to
// a destination id, creating the instance and destination rows if they do not
// exist yet. This is how a guild or room first becomes known to the bot.
func GetOrCreateDestinationByMeta(ctx context.Context, destination *pb.ReminderDestination) (int64, error) {
	platformEnum := destination.GetPlatformEnum()
	instanceMeta := destination.GetInstanceMeta()
	destinationMeta := destination.GetDestinationMeta()

	var destinationID int64
	err := db().QueryRow(ctx,
		`SELECT destination.id
		 FROM destination
		 JOIN instance ON destination.instance_id = instance.id
		 WHERE instance.platform_enum = $1
		   AND instance.instance_meta = $2
		   AND destination.destination_meta = $3
		   AND destination.deleted = FALSE`,
		platformEnum.Number(), instanceMeta, destinationMeta,
	).Scan(&destinationID)

	if err == nil {
		return destinationID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("scan destination: %w", err)
	}

	// No destination yet. The instance may or may not exist already.
	instanceID, err := GetOrCreateInstanceByMeta(ctx, platformEnum, instanceMeta)
	if err != nil {
		return 0, err
	}

	return CreateDestination(ctx, instanceID, destinationMeta)
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

// GetDestinationByID returns the destination row for id, or ErrNotFound.
func GetDestinationByID(ctx context.Context, id int64) (*model.Destination, error) {
	var destination model.Destination
	err := db().QueryRow(ctx,
		`SELECT `+model.DestinationColumns+` FROM destination WHERE id = $1 AND deleted = FALSE`,
		id,
	).Scan(destination.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan destination: %w", err)
	}

	return &destination, nil
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
