package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

func GetOrCreateDestinationByMeta(platformEnum *pb.Platform, instanceMeta *structpb.Struct, destinationMeta *structpb.Struct) (int64, error) {
	var platformID int64
	var destinationID int64

	err := db().QueryRow(
		context.Background(),
		`SELECT instance.id, destination.id  FROM destination
         LEFT JOIN instance ON destination.instance_id = instance.id
         WHERE instance.platform_enum = $1 AND instance.instance_meta = $2 AND destination.meta = $3`,
		platformEnum.Number(), instanceMeta, destinationMeta,
	).Scan(&platformID, &destinationID)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Creating a new destination if not found
			destinationID, err = CreateDestination(platformID, destinationMeta)
			if err != nil {
				log.Z.Error("failed to create destination", zap.Error(err))
			}
		} else {
			log.Z.Error("failed to scan platform", zap.Error(err))
		}
		return 0, err
	}

	return destinationID, nil
}

func CreateDestination(platformID int64, destinationMeta *structpb.Struct) (int64, error) {
	var destinationID int64
	err := db().QueryRow(
		context.Background(),
		"INSERT INTO destination (instance_id, meta) values($1, $2) RETURNING id",
		platformID, destinationMeta,
	).Scan(&destinationID)
	if err != nil {
		log.Z.Error("failed to insert destination", zap.Error(err))
		return 0, err
	}

	return destinationID, nil
}
