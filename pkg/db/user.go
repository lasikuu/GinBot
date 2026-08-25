package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// ErrAlreadyExists is returned when an insert violates a unique constraint.
var ErrAlreadyExists = errors.New("already exists")

// pgUniqueViolation is the SQLSTATE for a unique constraint violation.
const pgUniqueViolation = "23505"

// isUniqueViolation reports whether err is a unique constraint violation, so
// callers can map it to AlreadyExists instead of an opaque Internal.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

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

	// Clearance is set explicitly rather than left to the column default of 0
	// (CLEARANCE_UNSPECIFIED). Registering is what grants CLEARANCE_REGISTERED,
	// and without this every freshly registered user would sit below the minimum
	// the clearance interceptor enforces and be refused by every guarded RPC.
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_account (id, username, locale, clearance) VALUES ($1, $2, $3, $4)`,
		userID, username, nullStr(locale), pb.Clearance_CLEARANCE_REGISTERED.Number(),
	); err != nil {
		return "", fmt.Errorf("insert user_account: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO platform_user (user_id, platform_enum, platform_uid, user_meta)
		 VALUES ($1, $2, $3, $4)`,
		userID, platformEnum.Number(), platformUserID, userMetadata,
	); err != nil {
		if isUniqueViolation(err) {
			return "", fmt.Errorf("platform identity already registered: %w", ErrAlreadyExists)
		}
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

// SetUserLocale stores the display locale of a user_account row.
func SetUserLocale(ctx context.Context, userID string, locale string) error {
	return setUserPreference(ctx,
		`UPDATE user_account SET locale = $2 WHERE id = $1 AND deleted = FALSE`,
		userID, locale)
}

// SetUserTimezone stores the IANA timezone of a user_account row. The caller is
// responsible for having checked that the name resolves.
func SetUserTimezone(ctx context.Context, userID string, timezone string) error {
	return setUserPreference(ctx,
		`UPDATE user_account SET timezone = $2 WHERE id = $1 AND deleted = FALSE`,
		userID, timezone)
}

// setUserPreference runs a single-column update, reporting ErrNotFound when it
// matched nothing.
//
// updated_at is left alone on purpose: trg_user_account_updated_at advances it,
// so setting it here would be a second, divergent source of the same value.
func setUserPreference(ctx context.Context, query string, userID string, value string) error {
	tag, err := db().Exec(ctx, query, userID, nullStr(value))
	if err != nil {
		return fmt.Errorf("update user preference: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}
