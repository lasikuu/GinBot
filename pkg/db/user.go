package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// CreateUser inserts a user_account and its platform_user identity in one
// transaction, so a failure on the second insert cannot leave an orphaned
// account behind.
func CreateUser(
	ctx context.Context,
	username string,
	platformEnum pb.Platform,
	platformUserID string,
	userMetadata *structpb.Struct,
	locale string,
) (string, error) {
	userUUID, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate user uuid: %w", err)
	}
	userID := userUUID.String()

	tx, err := db().Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		// Rollback is a no-op once the transaction has been committed.
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			log.Z.Warn("failed to roll back user creation", zap.Error(err))
		}
	}()

	if _, err := tx.Exec(ctx,
		`INSERT INTO user_account (id, username, locale) VALUES ($1, $2, $3)`,
		userID, username, nullStr(locale),
	); err != nil {
		return "", fmt.Errorf("insert user_account: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO platform_user (user_id, platform_enum, platform_uid, user_meta)
		 VALUES ($1, $2, $3, $4)`,
		userID, platformEnum.Number(), platformUserID, userMetadata,
	); err != nil {
		return "", fmt.Errorf("insert platform_user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit user creation: %w", err)
	}

	return userID, nil
}

// GetUser returns the user_account row for id, or ErrNotFound.
func GetUser(ctx context.Context, id string) (*model.User, error) {
	var user model.User
	err := db().QueryRow(ctx,
		`SELECT `+model.UserColumns+` FROM user_account WHERE id = $1 AND deleted = FALSE`,
		id,
	).Scan(user.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}

	return &user, nil
}

// GetUserByPlatformUID resolves a platform-scoped identity to a user_account row.
func GetUserByPlatformUID(ctx context.Context, platformEnum pb.Platform, platformUID string) (*model.User, error) {
	var user model.User
	err := db().QueryRow(ctx,
		`SELECT `+prefixed(model.UserColumns, "user_account")+`
		 FROM user_account
		 JOIN platform_user ON user_account.id = platform_user.user_id
		 WHERE platform_user.platform_enum = $1
		   AND platform_user.platform_uid = $2
		   AND user_account.deleted = FALSE`,
		platformEnum.Number(), platformUID,
	).Scan(user.ScanTargets()...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user by platform uid: %w", err)
	}

	return &user, nil
}
