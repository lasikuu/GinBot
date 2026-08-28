//go:build integration

// Integration tests for the user surface, driven through the harness so
// the real interceptor chain and a real database are both in the picture.
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration ./pkg/grpc/server/...
//
// Connection settings come from the same GINBOT_DB_* variables the server uses.

package server

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
)

var (
	databaseOnce sync.Once
	databasePool *pgxpool.Pool
)

// requireDatabase brings up pkg/db's pool, plus a second pool of the test's own.
//
// TestMain already belongs to reverse_test.go and must stay database-free, so
// the setup is lazy. The second pool exists because pkg/db keeps its own pool
// unexported and these tests need raw SQL: to seed a clearance level, to read
// back what actually landed in user_account, and to delete rows afterwards.
func requireDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseOnce.Do(func() {
		config.LoadEnv()
		log.InitializeLogger(config.AppEnvironment, config.LogLevel)
		preserveObservedLogs()
		config.SetEnv()

		db.InitDB()
		db.EnsureLatestVersion()

		// Built the same way pkg/db builds it, so a password with reserved
		// characters is escaped rather than corrupting the URI.
		uri := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(config.Options.DB.Username, config.Options.DB.Password),
			Host:   net.JoinHostPort(config.Options.DB.Host, strconv.Itoa(int(config.Options.DB.Port))),
			Path:   config.Options.DB.Name,
		}

		pool, err := pgxpool.New(context.Background(), uri.String())
		if err != nil {
			t.Errorf("open test database pool: %v", err)
			return
		}
		databasePool = pool
	})

	if databasePool == nil {
		t.Fatal("no test database pool; is Postgres up?")
	}

	return databasePool
}

// liveHarness is a server wired to the real caller resolver.
func liveHarness(t *testing.T) (*harness, *pgxpool.Pool) {
	t.Helper()

	pool := requireDatabase(t)
	return newHarness(t, withResolver(db.GetUserByPlatformUID)), pool
}

// uniqueUID keeps identities from separate runs apart.
func uniqueUID(prefix string) string {
	return prefix + "-" + time.Now().Format("150405.000000")
}

// cleanupUser removes a user and its platform identities.
//
// platform_user.user_id has no ON DELETE CASCADE, so user_account has to go
// second or the foreign key rejects the delete. The errors are asserted rather
// than discarded: dropping them silently leaked rows on every run.
func cleanupUser(t *testing.T, pool *pgxpool.Pool, userID string) {
	t.Helper()

	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `DELETE FROM platform_user WHERE user_id = $1`, userID); err != nil {
			t.Errorf("cleanup platform_user for %s: %v", userID, err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM user_account WHERE id = $1`, userID); err != nil {
			t.Errorf("cleanup user_account %s: %v", userID, err)
		}
	})
}

// registerUser creates an account through the public Register RPC, the way a
// platform client does, and schedules its removal.
func registerUser(t *testing.T, h *harness, pool *pgxpool.Pool, platformUID string) string {
	t.Helper()

	username := "integration-" + platformUID
	locale := "en"

	resp, err := h.User.Register(
		callerCtx(pb.Platform_PLATFORM_DISCORD, platformUID),
		pb.RegisterReq_builder{Username: &username, Locale: &locale}.Build(),
	)
	if err != nil {
		t.Fatalf("Register(%s): %v", platformUID, err)
	}

	userID := resp.GetUserId()
	if userID == "" {
		t.Fatal("Register returned an empty user id")
	}
	cleanupUser(t, pool, userID)

	return userID
}

// setClearance writes a clearance level straight into the row. Nothing exposes
// a setter, and the tests that need an elevated caller are not about how
// clearance comes to be granted.
func setClearance(t *testing.T, pool *pgxpool.Pool, userID string, clearance pb.Clearance) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`UPDATE user_account SET clearance = $1 WHERE id = $2`,
		clearance.Number(), userID,
	); err != nil {
		t.Fatalf("set clearance for %s: %v", userID, err)
	}
}

// A second registration from the same platform identity is a user error — the
// client tells them they already have an account — so it must not surface as
// Internal, which is what an unmapped unique-constraint violation produces.
func TestRegisterTwiceReturnsAlreadyExists(t *testing.T) {
	h, pool := liveHarness(t)
	platformUID := uniqueUID("dup")

	registerUser(t, h, pool, platformUID)

	username := "second"
	locale := "en"
	_, err := h.User.Register(
		callerCtx(pb.Platform_PLATFORM_DISCORD, platformUID),
		pb.RegisterReq_builder{Username: &username, Locale: &locale}.Build(),
	)

	requireCode(t, err, connect.CodeAlreadyExists)

	// The failed attempt must not have left a second account behind.
	var accounts int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM platform_user WHERE platform_enum = $1 AND platform_uid = $2`,
		pb.Platform_PLATFORM_DISCORD.Number(), platformUID,
	).Scan(&accounts); err != nil {
		t.Fatalf("count platform identities: %v", err)
	}
	if accounts != 1 {
		t.Errorf("platform_user rows = %d, want 1", accounts)
	}
}

// Registration has to grant CLEARANCE_REGISTERED. The column defaults to 0,
// which is CLEARANCE_UNSPECIFIED, and a user sitting at 0 fails every guarded
// method — so a freshly registered account would be unable to do anything at
// all, including set its own locale.
func TestRegisterGrantsRegisteredClearance(t *testing.T) {
	h, pool := liveHarness(t)

	userID := registerUser(t, h, pool, uniqueUID("clearance"))

	var clearance int32
	if err := pool.QueryRow(context.Background(),
		`SELECT clearance FROM user_account WHERE id = $1`, userID,
	).Scan(&clearance); err != nil {
		t.Fatalf("read clearance: %v", err)
	}

	if clearance < int32(pb.Clearance_CLEARANCE_REGISTERED) {
		t.Errorf("clearance after Register = %d, want at least %d (CLEARANCE_REGISTERED)",
			clearance, pb.Clearance_CLEARANCE_REGISTERED)
	}
}

// Neither request carries a user id: the subject is the caller, taken from
// metadata. This is the end-to-end proof that a sufficiently cleared caller
// gets through the chain and that the write reaches the row.
func TestLocaleAndTimezoneRoundTripThroughUserAccount(t *testing.T) {
	h, pool := liveHarness(t)
	platformUID := uniqueUID("prefs")

	userID := registerUser(t, h, pool, platformUID)
	setClearance(t, pool, userID, pb.Clearance_CLEARANCE_REGISTERED)

	ctx := callerCtx(pb.Platform_PLATFORM_DISCORD, platformUID)

	// Registered with "en", so setting "fi" also proves this updates rather
	// than only ever inserting.
	locale := "fi"
	if _, err := h.User.SetLocale(ctx, pb.SetLocaleReq_builder{Locale: &locale}.Build()); err != nil {
		t.Fatalf("SetLocale: %v", err)
	}

	timezone := "Europe/Helsinki"
	if _, err := h.User.SetTimezone(ctx, pb.SetTimezoneReq_builder{Timezone: &timezone}.Build()); err != nil {
		t.Fatalf("SetTimezone: %v", err)
	}

	var storedLocale, storedTimezone *string
	if err := pool.QueryRow(context.Background(),
		`SELECT locale, timezone FROM user_account WHERE id = $1`, userID,
	).Scan(&storedLocale, &storedTimezone); err != nil {
		t.Fatalf("read preferences: %v", err)
	}

	if storedLocale == nil || *storedLocale != locale {
		t.Errorf("user_account.locale = %v, want %q", storedLocale, locale)
	}
	if storedTimezone == nil || *storedTimezone != timezone {
		t.Errorf("user_account.timezone = %v, want %q", storedTimezone, timezone)
	}

	// And the same values must come back out through the API.
	resp, err := h.User.GetUser(ctx, pb.GetUserReq_builder{Id: &userID}.Build())
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got := resp.GetUser().GetLocale(); got != locale {
		t.Errorf("GetUser locale = %q, want %q", got, locale)
	}
	if got := resp.GetUser().GetTimezone(); got != timezone {
		t.Errorf("GetUser timezone = %q, want %q", got, timezone)
	}

	// A second change must stick too.
	locale = "ja"
	if _, err := h.User.SetLocale(ctx, pb.SetLocaleReq_builder{Locale: &locale}.Build()); err != nil {
		t.Fatalf("SetLocale (second): %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT locale FROM user_account WHERE id = $1`, userID,
	).Scan(&storedLocale); err != nil {
		t.Fatalf("read locale again: %v", err)
	}
	if storedLocale == nil || *storedLocale != locale {
		t.Errorf("user_account.locale = %v, want %q", storedLocale, locale)
	}
}

// A user row carries locale, timezone and birthday, so it is private. Only
// moderation-level clearance has a reason to read someone else's.
func TestGetUserRefusesAnotherUserBelowModerator(t *testing.T) {
	h, pool := liveHarness(t)

	callerPlatformUID := uniqueUID("reader")
	callerID := registerUser(t, h, pool, callerPlatformUID)
	subjectID := registerUser(t, h, pool, uniqueUID("subject"))

	ctx := callerCtx(pb.Platform_PLATFORM_DISCORD, callerPlatformUID)

	for _, clearance := range []pb.Clearance{
		pb.Clearance_CLEARANCE_REGISTERED,
		pb.Clearance_CLEARANCE_MEMBER,
	} {
		t.Run(clearance.String(), func(t *testing.T) {
			setClearance(t, pool, callerID, clearance)

			_, err := h.User.GetUser(ctx, pb.GetUserReq_builder{Id: &subjectID}.Build())
			requireCode(t, err, connect.CodePermissionDenied)
		})
	}

	// Their own row stays readable at the lowest clearance.
	t.Run("own row", func(t *testing.T) {
		setClearance(t, pool, callerID, pb.Clearance_CLEARANCE_REGISTERED)

		resp, err := h.User.GetUser(ctx, pb.GetUserReq_builder{Id: &callerID}.Build())
		if err != nil {
			t.Fatalf("GetUser on own row: %v", err)
		}
		if got := resp.GetUser().GetId(); got != callerID {
			t.Errorf("id = %q, want %q", got, callerID)
		}
	})
}

// The other side of the same boundary.
func TestGetUserAllowsAnotherUserAtModerator(t *testing.T) {
	h, pool := liveHarness(t)

	callerPlatformUID := uniqueUID("moderator")
	callerID := registerUser(t, h, pool, callerPlatformUID)
	subjectID := registerUser(t, h, pool, uniqueUID("moderated"))

	setClearance(t, pool, callerID, pb.Clearance_CLEARANCE_MODERATOR)

	resp, err := h.User.GetUser(
		callerCtx(pb.Platform_PLATFORM_DISCORD, callerPlatformUID),
		pb.GetUserReq_builder{Id: &subjectID}.Build(),
	)
	if err != nil {
		t.Fatalf("GetUser as moderator: %v", err)
	}
	if got := resp.GetUser().GetId(); got != subjectID {
		t.Errorf("id = %q, want %q", got, subjectID)
	}
}

// With the real resolver behind it, an unknown platform identity must produce
// the "you are not registered" code rather than an Internal from the failed
// lookup.
func TestGuardedRPCRejectsAnUnknownIdentity(t *testing.T) {
	h, _ := liveHarness(t)

	locale := "en"
	_, err := h.User.SetLocale(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uniqueUID("ghost")),
		pb.SetLocaleReq_builder{Locale: &locale}.Build(),
	)

	requireCode(t, err, connect.CodeFailedPrecondition)
}
