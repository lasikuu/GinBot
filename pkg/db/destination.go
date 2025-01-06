package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/lasikuu/GinBot/pkg/gen/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

func GetOrCreateDestinationByMeta(platformEnum *proto.PlatformEnum, platformMeta *structpb.Struct, destinationMeta *structpb.Struct) (int64, error) {
	var platformID int64
	var destinationID int64

	err := db().QueryRow(
		context.Background(),
		`SELECT platform.id, destination.id  FROM destination
         LEFT JOIN platform ON destination.platform_id = platform.id
         WHERE platform.enum = $1 AND platform.meta = $2 AND destination.meta = $3`,
		platformEnum.Number(), platformMeta, destinationMeta,
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
		"INSERT INTO destination (platform_id, meta) values($1, $2) RETURNING id",
		platformID, destinationMeta,
	).Scan(&destinationID)
	if err != nil {
		log.Z.Error("failed to insert destination", zap.Error(err))
		return 0, err
	}

	return destinationID, nil
}
