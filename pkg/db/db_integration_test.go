//go:build integration

// The suite runs against a throwaway database created per run: the claim and
// reclaim sweeps are table-global, and `go test ./...` runs packages
// concurrently against one Postgres.
package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMain(m *testing.M) {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	config.SetEnv()

	// CREATE DATABASE and DROP DATABASE cannot run from inside the database they
	// name, so the throwaway is created and dropped through this pool.
	InitDB()
	sharedPool := dbpool

	// Without migrations every test fails on empty schema with a misleading
	// "relation does not exist".
	if !config.Options.DB.Migrations {
		fatalf("GINBOT_DB_MIGRATIONS is disabled, so the throwaway database cannot be migrated; " +
			"unset it or set it to anything but the exact literal \"false\" to run the integration suite")
	}

	// A panic or log.Z.Fatal leaves through os.Exit and skips the teardown below,
	// so leaked databases accumulate until Postgres refuses connections.
	dropLeakedSuiteDatabases(sharedPool)

	// Lowercase and unpunctuated so it needs no quoting as an identifier; the pid
	// separates two runs starting in the same nanosecond.
	suiteDatabase := fmt.Sprintf("ginbot_suite_%d_%d", os.Getpid(), time.Now().UnixNano())

	if _, err := sharedPool.Exec(context.Background(), `CREATE DATABASE `+suiteDatabase); err != nil {
		fatalf("create throwaway database %s: %v", suiteDatabase, err)
	}

	suitePool, err := pgxpool.New(context.Background(), migrationTestDSN(suiteDatabase))
	if err != nil {
		fatalf("open throwaway database %s: %v", suiteDatabase, err)
	}

	// EnsureLatestVersion migrates whatever db() points at, so the swap comes
	// first or the configured database is migrated instead.
	dbpool = suitePool
	EnsureLatestVersion()

	// os.Exit skips defers, so the code is captured and teardown run by hand.
	code := m.Run()

	// DROP DATABASE refuses while a session is connected, so the throwaway's pool
	// closes first; FORCE covers a connection it has not yet released.
	dbpool = sharedPool
	suitePool.Close()

	if _, err := sharedPool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+suiteDatabase+` WITH (FORCE)`); err != nil {
		log.Z.Error("failed to drop the throwaway database; it has been LEAKED and must be dropped by hand")
		fmt.Fprintf(os.Stderr, "drop throwaway database %s: %v\n", suiteDatabase, err)
		code = 1
	}

	CloseDB()

	os.Exit(code)
}

// fatalf reports a setup failure to stderr, where CI output will show it.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pkg/db integration setup: "+format+"\n", args...)
	os.Exit(1)
}

// dropLeakedSuiteDatabases is best effort and must never fail the suite. DROP
// without FORCE errors while sessions are attached, which is what skips a
// database a concurrently running suite is still using.
func dropLeakedSuiteDatabases(pool *pgxpool.Pool) {
	ctx := context.Background()

	rows, err := pool.Query(ctx,
		`SELECT datname FROM pg_database
		  WHERE datname LIKE 'ginbot\_suite\_%' OR datname LIKE 'ginbot\_migtest\_%'`)
	if err != nil {
		log.Z.Warn("could not look for leaked throwaway databases", zap.Error(err))
		return
	}

	var leaked []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			log.Z.Warn("could not read a leaked throwaway database name", zap.Error(err))
			rows.Close()
			return
		}
		leaked = append(leaked, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Z.Warn("could not list leaked throwaway databases", zap.Error(err))
		return
	}

	for _, name := range leaked {
		// Interpolation is safe: these names came from pg_database and matched the
		// two literal prefixes above.
		if _, err := pool.Exec(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
			log.Z.Debug("left a throwaway database in place; it is most likely in use by a concurrent run",
				zap.String("database", name), zap.Error(err))
			continue
		}
		log.Z.Info("dropped a throwaway database leaked by an earlier run", zap.String("database", name))
	}
}

func meta(t *testing.T, kv map[string]string) *structpb.Struct {
	t.Helper()
	fields := make(map[string]any, len(kv))
	for k, v := range kv {
		fields[k] = v
	}
	s, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatalf("build struct: %v", err)
	}
	return s
}

// cleanupUser deletes platform_user first: user_id has no ON DELETE CASCADE, so
// removing user_account first always fails the foreign key.
func cleanupUser(t *testing.T, userID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := db().Exec(ctx, `DELETE FROM platform_user WHERE user_id = $1`, userID); err != nil {
			t.Errorf("cleanup platform_user for %s: %v", userID, err)
		}
		if _, err := db().Exec(ctx, `DELETE FROM user_account WHERE id = $1`, userID); err != nil {
			t.Errorf("cleanup user_account %s: %v", userID, err)
		}
	})
}

func cleanupInstanceByMeta(t *testing.T, instanceMeta *structpb.Struct) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := db().Exec(ctx,
			`DELETE FROM reminder WHERE destination_id IN (
			     SELECT d.id FROM destination d JOIN instance i ON d.instance_id = i.id
			     WHERE i.instance_meta = $1)`, instanceMeta); err != nil {
			t.Errorf("cleanup reminders: %v", err)
		}
		if _, err := db().Exec(ctx,
			`DELETE FROM destination WHERE instance_id IN (
			     SELECT id FROM instance WHERE instance_meta = $1)`, instanceMeta); err != nil {
			t.Errorf("cleanup destinations: %v", err)
		}
		if _, err := db().Exec(ctx, `DELETE FROM instance WHERE instance_meta = $1`, instanceMeta); err != nil {
			t.Errorf("cleanup instances: %v", err)
		}
	})
}

func TestCreateAndGetUser(t *testing.T) {
	ctx := context.Background()
	platformUID := "test-uid-" + time.Now().Format("150405.000000")

	userID, err := CreateUser(ctx, "integration-user", pb.Platform_PLATFORM_DISCORD,
		platformUID, meta(t, map[string]string{"source": "test"}), "en")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cleanupUser(t, userID)

	user, err := GetUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.Username != "integration-user" {
		t.Errorf("username = %q, want %q", user.Username, "integration-user")
	}
	if user.Locale == nil || *user.Locale != "en" {
		t.Errorf("locale = %v, want en", user.Locale)
	}
	if user.CreatedAt.IsZero() {
		t.Error("created_at was not scanned")
	}

	if got := user.ToProto().GetId(); got != userID {
		t.Errorf("ToProto id = %q, want %q", got, userID)
	}

	byUID, err := GetUserByPlatformUID(ctx, pb.Platform_PLATFORM_DISCORD, platformUID)
	if err != nil {
		t.Fatalf("GetUserByPlatformUID: %v", err)
	}
	if byUID.ID != userID {
		t.Errorf("lookup by uid returned %q, want %q", byUID.ID, userID)
	}
}

func TestGetUserNotFound(t *testing.T) {
	_, err := GetUser(context.Background(), "00000000-0000-7000-8000-00000000ffff")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateUserIsAtomic(t *testing.T) {
	ctx := context.Background()
	platformUID := "dup-uid-" + time.Now().Format("150405.000000")

	firstID, err := CreateUser(ctx, "first", pb.Platform_PLATFORM_DISCORD, platformUID, nil, "en")
	if err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	cleanupUser(t, firstID)

	var before int
	if err := db().QueryRow(ctx, `SELECT COUNT(*) FROM user_account`).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}

	// platform_user has UNIQUE(platform_enum, platform_uid), so this must fail.
	if _, err := CreateUser(ctx, "second", pb.Platform_PLATFORM_DISCORD, platformUID, nil, "en"); err == nil {
		t.Fatal("expected duplicate platform identity to fail")
	}

	var after int
	if err := db().QueryRow(ctx, `SELECT COUNT(*) FROM user_account`).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Errorf("user_account count changed from %d to %d; insert was not rolled back", before, after)
	}
}

func TestGetOrCreateDestinationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")

	platform := pb.Platform_PLATFORM_DISCORD
	destination := pb.ReminderDestination_builder{
		PlatformEnum:    &platform,
		InstanceMeta:    meta(t, map[string]string{"guild_id": "g-" + suffix}),
		DestinationMeta: meta(t, map[string]string{"channel_id": "c-" + suffix}),
	}.Build()

	cleanupInstanceByMeta(t, destination.GetInstanceMeta())

	first, err := GetOrCreateDestinationByMeta(ctx, destination)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first == 0 {
		t.Fatal("returned destination id 0")
	}

	second, err := GetOrCreateDestinationByMeta(ctx, destination)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != second {
		t.Errorf("not idempotent: got %d then %d", first, second)
	}

	var instances int
	if err := db().QueryRow(ctx,
		`SELECT COUNT(*) FROM instance WHERE instance_meta = $1`,
		destination.GetInstanceMeta(),
	).Scan(&instances); err != nil {
		t.Fatalf("count instances: %v", err)
	}
	if instances != 1 {
		t.Errorf("instance count = %d, want 1", instances)
	}

	if _, err := GetReminderDestination(ctx, first); err != nil {
		t.Errorf("GetReminderDestination: %v", err)
	}
}

func TestCreateAndReadReminder(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")

	userID, err := CreateUser(ctx, "reminder-owner", pb.Platform_PLATFORM_DISCORD, "ru-"+suffix, nil, "en")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cleanupUser(t, userID)

	origin := callermeta.Origin{InstanceUID: "rg-" + suffix, DestinationUID: "rc-" + suffix}
	destination := pb.ReminderDestination_builder{
		PlatformEnum:    pb.Platform_PLATFORM_DISCORD.Enum(),
		InstanceMeta:    origin.InstanceMeta(),
		DestinationMeta: origin.DestinationMeta(),
	}.Build()

	cleanupInstanceByMeta(t, destination.GetInstanceMeta())

	destinationID, err := GetOrCreateDestinationByMeta(ctx, destination)
	if err != nil {
		t.Fatalf("destination: %v", err)
	}

	fireAt := time.Now().Add(-time.Minute).UTC()
	message := "integration reminder"
	timezone := "Europe/Helsinki"

	req := pb.CreateReminderReq_builder{
		Datetime:    timestamppb.New(fireAt),
		Timezone:    &timezone,
		Message:     &message,
		Destination: destination,
	}.Build()

	reminderID, err := CreateReminder(ctx, req, userID, destinationID, fixtureReminderCap)
	if err != nil {
		t.Fatalf("CreateReminder: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(),
			`DELETE FROM reminder WHERE id = $1`, reminderID); err != nil {
			t.Errorf("cleanup reminder %s: %v", reminderID, err)
		}
	})

	reminder, err := GetReminder(ctx, reminderID)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if reminder.Message == nil || *reminder.Message != message {
		t.Errorf("message = %v, want %q", reminder.Message, message)
	}
	if reminder.Timezone != timezone {
		t.Errorf("timezone = %q, want %q", reminder.Timezone, timezone)
	}
	if reminder.UserID == nil || *reminder.UserID != userID {
		t.Errorf("user_id = %v, want %q", reminder.UserID, userID)
	}
	if reminder.RepeatCron != nil {
		t.Errorf("repeat_cron = %v, want nil", *reminder.RepeatCron)
	}
	if reminder.ParentID != nil {
		t.Errorf("parent_id = %v, want nil", *reminder.ParentID)
	}
	if got := reminder.Datetime.UTC().Truncate(time.Second); !got.Equal(fireAt.Truncate(time.Second)) {
		t.Errorf("datetime = %v, want %v", got, fireAt.Truncate(time.Second))
	}

	proto := reminder.ToProto(destination)
	if proto.GetId() != reminderID {
		t.Errorf("ToProto id = %q, want %q", proto.GetId(), reminderID)
	}

	claimed, err := ClaimDueReminders(ctx, time.Now())
	if err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}
	found := false
	for _, r := range claimed {
		if r.ID == reminderID {
			found = true
			break
		}
	}
	if !found {
		t.Error("newly created past-due reminder was not claimed")
	}

	if err := SetReminderStatus(ctx, reminderID, pb.ReminderStatus_REMINDER_STATUS_DELIVERED); err != nil {
		t.Fatalf("SetReminderStatus: %v", err)
	}
	updated, err := GetReminder(ctx, reminderID)
	if err != nil {
		t.Fatalf("GetReminder after status change: %v", err)
	}
	if updated.Status != int32(pb.ReminderStatus_REMINDER_STATUS_DELIVERED.Number()) {
		t.Errorf("status = %d, want %d", updated.Status, pb.ReminderStatus_REMINDER_STATUS_DELIVERED.Number())
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Error("updated_at did not advance on UPDATE; trigger missing")
	}
}

// The unique indexes plus ON CONFLICT are what stop concurrent callers all
// missing the SELECT and each inserting a duplicate row.
func TestGetOrCreateDestinationIsRaceSafe(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")

	platform := pb.Platform_PLATFORM_DISCORD
	instanceMeta := meta(t, map[string]string{"guild_id": "race-" + suffix})
	destination := pb.ReminderDestination_builder{
		PlatformEnum:    &platform,
		InstanceMeta:    instanceMeta,
		DestinationMeta: meta(t, map[string]string{"channel_id": "race-c-" + suffix}),
	}.Build()

	cleanupInstanceByMeta(t, instanceMeta)

	const concurrency = 16
	ids := make([]int64, concurrency)
	errs := make([]error, concurrency)

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < concurrency; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait() // release all goroutines together to maximise contention
			ids[i], errs[i] = GetOrCreateDestinationByMeta(ctx, destination)
		}(i)
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	for i, id := range ids {
		if id != ids[0] {
			t.Errorf("goroutine %d got destination %d, goroutine 0 got %d", i, id, ids[0])
		}
	}

	var instances, destinations int
	if err := db().QueryRow(ctx,
		`SELECT COUNT(*) FROM instance WHERE instance_meta = $1`, instanceMeta,
	).Scan(&instances); err != nil {
		t.Fatalf("count instances: %v", err)
	}
	if err := db().QueryRow(ctx,
		`SELECT COUNT(*) FROM destination d JOIN instance i ON d.instance_id = i.id
		 WHERE i.instance_meta = $1`, instanceMeta,
	).Scan(&destinations); err != nil {
		t.Fatalf("count destinations: %v", err)
	}

	if instances != 1 {
		t.Errorf("instance rows = %d, want 1", instances)
	}
	if destinations != 1 {
		t.Errorf("destination rows = %d, want 1", destinations)
	}
}

// Uniqueness is on (platform_enum, instance_meta): a Discord guild id and a
// Matrix room id could collide as strings and share one instance row.
func TestGetOrCreateDestinationIsScopedToPlatform(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")

	instanceMeta := meta(t, map[string]string{"guild_id": "scoped-" + suffix})
	destinationMeta := meta(t, map[string]string{"channel_id": "scoped-c-" + suffix})

	cleanupInstanceByMeta(t, instanceMeta)

	destinationFor := func(platform pb.Platform) *pb.ReminderDestination {
		return pb.ReminderDestination_builder{
			PlatformEnum:    platform.Enum(),
			InstanceMeta:    instanceMeta,
			DestinationMeta: destinationMeta,
		}.Build()
	}

	discord, err := GetOrCreateDestinationByMeta(ctx, destinationFor(pb.Platform_PLATFORM_DISCORD))
	if err != nil {
		t.Fatalf("discord: %v", err)
	}

	matrix, err := GetOrCreateDestinationByMeta(ctx, destinationFor(pb.Platform_PLATFORM_MATRIX_PROTOCOL))
	if err != nil {
		t.Fatalf("matrix: %v", err)
	}

	if discord == matrix {
		t.Errorf("both platforms resolved to destination %d; the platform is not part of the key", discord)
	}

	var instances int
	if err := db().QueryRow(ctx,
		`SELECT COUNT(*) FROM instance WHERE instance_meta = $1`, instanceMeta,
	).Scan(&instances); err != nil {
		t.Fatalf("count instances: %v", err)
	}
	if instances != 2 {
		t.Errorf("instance rows = %d, want 2 (one per platform)", instances)
	}
}

// Ping backs all three health surfaces, so it must answer against a real pool.
// The failure branch is covered by the HealthProbe fakes, since failing Ping here
// would mean closing the pool the rest of the suite shares.
func TestPingReportsAReachablePool(t *testing.T) {
	if err := Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestGetInstanceByMetaMatchesOnPlatformAndMeta(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")
	instanceMeta := meta(t, map[string]string{"guild_id": "im-" + suffix})

	cleanupInstanceByMeta(t, instanceMeta)

	id, err := CreateInstance(ctx, pb.Platform_PLATFORM_DISCORD, instanceMeta, "general")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	got, err := GetInstanceByMeta(ctx, pb.Platform_PLATFORM_DISCORD, instanceMeta)
	if err != nil {
		t.Fatalf("GetInstanceByMeta: %v", err)
	}
	if got.ID != id {
		t.Errorf("id = %d, want %d", got.ID, id)
	}
	if got.DefaultChannel == nil || *got.DefaultChannel != "general" {
		t.Errorf("default_channel = %v, want general", got.DefaultChannel)
	}

	if _, err := GetInstanceByMeta(ctx, pb.Platform_PLATFORM_MATRIX_PROTOCOL, instanceMeta); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-platform lookup err = %v, want ErrNotFound", err)
	}
}
