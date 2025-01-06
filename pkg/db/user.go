package db

import (
	"context"

	"github.com/google/uuid"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

func CreateUser(username string, platformEnum pb.Platform, platformUserId string, userMetadata *structpb.Struct, locale *string) (*string, error) {
	userUUID, err := uuid.NewV7()
	if err != nil {
		log.Z.Error("failed to generate UUID.", zap.Error(err))
		return nil, err
	}
	userID := userUUID.String()

	_, err = db().Exec(
		context.Background(),
		"INSERT INTO user_account (id, username, locale) values($1, $2, $3)",
		userID, username, locale,
	)
	if err != nil {
		log.Z.Error("failed to insert user", zap.Error(err))
		return nil, err
	}

	_, err = db().Exec(
		context.Background(),
		"INSERT INTO platform_user (user_id, platform_enum, platform_uid, user_meta) values($1, $2, $3, $4)",
		userID, platformEnum, platformUserId, userMetadata,
	)
	if err != nil {
		log.Z.Error("failed to insert platform user", zap.Error(err))
		return nil, err
	}

	return &userID, err
}

func GetUser(id string) *pb.User {
	var user pb.User
	err := db().QueryRow(
		context.Background(),
		"SELECT * FROM user_account WHERE id = $1", id,
	).Scan(&user)

	if err != nil {
		log.Z.Debug("failed to scan user", zap.Error(err))
		return nil
	}

	return &user
}

func GetUserByPlatformUID(platformEnum pb.Platform, platformUID string) (string, string, error) {
	var userID string
	var username string

	err := db().QueryRow(
		context.Background(),
		`SELECT user_account.id, user_account.username
			FROM user_account JOIN platform_user ON user_account.id = platform_user.user_id
			WHERE platform_user.platform_enum = $1 AND platform_user.platform_uid = $2`,
		platformEnum, platformUID,
	).Scan(&userID, &username)
	if err != nil {
		log.Z.Debug("failed to scan user", zap.Error(err))
		return "", "", err
	}

	return userID, username, nil
}
