//go:build integration

// Integration tests for the database layer. These require a live Postgres.
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration ./pkg/db/...
//
// Connection settings come from the same GINBOT_DB_* environment variables the
// server uses.
package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/internal/config"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMain(m *testing.M) {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	config.SetEnv()

	InitDB()
	EnsureLatestVersion()
	defer CloseDB()

	m.Run()
}

// meta builds a structpb.Struct from string key/values.
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

// cleanupUser removes a user and its platform identities.
//
// platform_user.user_id has no ON DELETE CASCADE, so deleting user_account
// first always fails the foreign key. Errors are asserted rather than
// discarded — silently ignoring them leaked rows on every run.
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

// cleanupInstanceByMeta removes an instance and everything hanging off it.
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

	// ToProto must not panic and must carry the id through.
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

// CreateUser must be atomic: a duplicate platform identity has to roll the
// user_account insert back rather than leaving an orphan behind.
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

	// The instance must have been created exactly once, too.
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

	// The instance and destination metadata use the canonical shapes from
	// callermeta, which is what production writes, so the jsonb this row is
	// resolved through is the same jsonb the delivery path reads.
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

	// Already in the past, so the delivery loop's claim would pick it up.
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
	// repeat_cron and parent_id were unset and must be NULL, not "".
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

	// The past-due reminder is picked up by the delivery loop's claim. It used to
	// be checked through db.ExpiredReminders, which has been deleted: it returned
	// every user's due reminders — message text and resolved destination — with no
	// user_id predicate, and the cron loop uses ClaimDueReminders instead.
	// ClaimDueReminders has its own dedicated tests in reminder_integration_test.go;
	// this only confirms the freshly written row is visible to it.
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
	// The updated_at trigger added by the schema-defect migration must have fired.
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Error("updated_at did not advance on UPDATE; trigger missing")
	}
}

// Regression test for the get-or-create race. Before the unique indexes and the
// ON CONFLICT inserts, concurrent callers for the same channel all missed the
// SELECT and each inserted, producing duplicate instance and destination rows
// that later lookups would pick from arbitrarily.
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

	// Every caller must observe the same destination.
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

// The uniqueness that makes the bootstrap idempotent is on
// (platform_enum, instance_meta). A Discord guild id and a Matrix room id could
// collide as strings, so the platform has to be part of the key or the two
// would share one instance row.
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

	// Same meta under a different platform must not match.
	if _, err := GetInstanceByMeta(ctx, pb.Platform_PLATFORM_MATRIX_PROTOCOL, instanceMeta); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-platform lookup err = %v, want ErrNotFound", err)
	}
}
