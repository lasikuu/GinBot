package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/lasikuu/GinBot/pkg/gen/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

func CreateUser(username string, platform proto.Platform, platformUserId string, platformMetadata *structpb.Struct, locale *string) (*string, error) {
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
		log.Z.Error("failed to insert user.", zap.Error(err))
		return nil, err
	}

	_, err = db().Exec(
		context.Background(),
		"INSERT INTO platform_user (user_id, platform, platform_user_id, metadata) values($1, $2, $3, $4)",
		userID, platform, platformUserId, platformMetadata,
	)
	if err != nil {
		log.Z.Error("failed to insert platform user.", zap.Error(err))
		return nil, err
	}

	return &userID, err
}

func GetUser(id string) *proto.UserAccount {
	var user proto.UserAccount
	err := db().QueryRow(
		context.Background(),
		"SELECT * FROM user_account WHERE id = $1", id,
	).Scan(&user)

	if err != nil {
		log.Z.Debug("failed to scan user.", zap.Error(err))
		return nil
	}

	return &user
}

func CongratulableBirthdays(now *time.Time) ([]*proto.Reminder, error) {
	return nil, nil
}
