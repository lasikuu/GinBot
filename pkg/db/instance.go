package db

import (
	"context"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

func CreateInstance(platformEnum pb.Platform, platformMeta *structpb.Struct, defaultChannel *string) (int64, error) {
	var platformID int64
	err := db().QueryRow(
		context.Background(),
		"INSERT INTO instance (platform_enum, instance_meta, default_channel) values($1, $2, $3) RETURNING id",
		platformEnum.Number(), platformMeta, defaultChannel,
	).Scan(&platformID)
	if err != nil {
		log.Z.Error("failed to insert platform", zap.Error(err))
		return 0, err
	}

	return platformID, nil
}

func GetInstanceByID(id int64) (*pb.Instance, error) {
	var instance pb.Instance
	err := db().QueryRow(
		context.Background(),
		"SELECT * FROM instance WHERE id = $1", id,
	).Scan(&instance)
	if err != nil {
		log.Z.Error("failed to scan platform", zap.Error(err))
		return nil, err
	}

	return &instance, nil
}

func GetInstanceByMeta(platformEnum pb.Platform, platformMeta *structpb.Struct) (*pb.Instance, error) {
	var instance pb.Instance
	err := db().QueryRow(
		context.Background(),
		"SELECT * FROM instance WHERE instance_meta = $1 AND instance_meta = $2",
		platformEnum.Number(), platformMeta,
	).Scan(&instance)
	if err != nil {
		log.Z.Error("failed to scan platform", zap.Error(err))
		return nil, err
	}

	return &instance, nil
}
