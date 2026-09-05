//go:build integration

package server

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/reminder"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// liveReminderHarness wires both resolvers to the real database, because
// CreateReminder bootstraps a destination through the origin one.
func liveReminderHarness(t *testing.T) (*harness, *pgxpool.Pool) {
	t.Helper()
	pool := requireDatabase(t)
	return newHarness(t,
		withResolver(db.GetUserByPlatformUID),
		withOriginResolver(db.GetOrCreateDestinationByMeta),
	), pool
}

// withOriginResolver injects a real OriginResolver; the harness default writes no rows.
func withOriginResolver(resolve interceptor.OriginResolver) harnessOption {
	return func(cfg *harnessConfig) { cfg.resolveOrigin = resolve }
}

// originFor builds a unique instance/destination identity. The jsonb shapes come from
// callermeta.Origin: hand-spelled keys make every destination look like a DM.
func originFor(suffix string) callermeta.Origin {
	return callermeta.Origin{
		InstanceUID:    "instance-" + suffix,
		DestinationUID: "destination-" + suffix,
	}
}

// destinationFor also schedules cleanup of the instance and destination rows it causes.
func destinationFor(t *testing.T, pool *pgxpool.Pool, suffix string) *pb.ReminderDestination {
	t.Helper()
	origin := originFor(suffix)
	instanceMeta := origin.InstanceMeta()

	// Registered before the reminder's own cleanup so LIFO runs it after: NO ACTION FK.
	cleanupInstanceRows(t, pool, instanceMeta)

	return pb.ReminderDestination_builder{
		PlatformEnum:    pb.Platform_PLATFORM_DISCORD.Enum(),
		InstanceMeta:    instanceMeta,
		DestinationMeta: origin.DestinationMeta(),
	}.Build()
}

// cleanupInstanceRows deletes reminders, destination and instance, innermost first.
// Reminders go first regardless of LIFO, because fk_reminder_destination is NO ACTION.
func cleanupInstanceRows(t *testing.T, pool *pgxpool.Pool, instanceMeta *structpb.Struct) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx,
			`DELETE FROM reminder WHERE destination_id IN (
			     SELECT d.id FROM destination d JOIN instance i ON d.instance_id = i.id
			     WHERE i.instance_meta = $1)`, instanceMeta); err != nil {
			t.Errorf("cleanup reminders for instance: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM destination WHERE instance_id IN (
			     SELECT id FROM instance WHERE instance_meta = $1)`, instanceMeta); err != nil {
			t.Errorf("cleanup destinations: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM instance WHERE instance_meta = $1`, instanceMeta); err != nil {
			t.Errorf("cleanup instance: %v", err)
		}
	})
}

func createReminderVia(t *testing.T, h *harness, pool *pgxpool.Pool, platformUID, suffix, repeatCron string) string {
	t.Helper()
	ctx := callerCtx(pb.Platform_PLATFORM_DISCORD, platformUID)

	future := time.Now().Add(time.Hour).UTC()
	message := "reminder-" + suffix
	tz := "Europe/Helsinki"
	b := pb.CreateReminderReq_builder{
		Datetime:    timestamppb.New(future),
		Timezone:    &tz,
		Message:     &message,
		Destination: destinationFor(t, pool, suffix),
	}
	if repeatCron != "" {
		b.RepeatCron = &repeatCron
	}
	resp, err := h.Reminder.CreateReminder(ctx, b.Build())
	if err != nil {
		t.Fatalf("CreateReminder: %v", err)
	}
	id := resp.GetId()
	if id == "" {
		t.Fatal("CreateReminder returned empty id")
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM reminder WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup reminder %s: %v", id, err)
		}
	})
	return id
}

// markSent makes a reminder due and SENT with a claim stamp, as ClaimDueReminders would.
func markSent(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE reminder
		    SET datetime = $1, status = $2, claimed_at = $3, delivery_attempts = delivery_attempts + 1
		  WHERE id = $4`,
		time.Now().Add(-time.Minute).UTC(),
		int32(pb.ReminderStatus_REMINDER_STATUS_SENT.Number()),
		time.Now().UTC(),
		id,
	); err != nil {
		t.Fatalf("mark reminder due+sent: %v", err)
	}
}

func reminderState(t *testing.T, pool *pgxpool.Pool, id string) (status int32, datetime time.Time, claimedAt *time.Time) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT status, datetime, claimed_at FROM reminder WHERE id = $1`, id,
	).Scan(&status, &datetime, &claimedAt); err != nil {
		t.Fatalf("read reminder %s: %v", id, err)
	}
	return status, datetime, claimedAt
}

func countActionRecords(t *testing.T, pool *pgxpool.Pool, actorID string, actionType pb.ActionType) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM action_record WHERE actor_id = $1 AND action_type = $2`,
		actorID, int32(actionType.Number()),
	).Scan(&count); err != nil {
		t.Fatalf("count action records: %v", err)
	}
	return count
}

func cleanupActionRecords(t *testing.T, pool *pgxpool.Pool, actorID string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM action_record WHERE actor_id = $1`, actorID); err != nil {
			t.Errorf("cleanup action_record for %s: %v", actorID, err)
		}
	})
}

func registeredCaller(t *testing.T, h *harness, pool *pgxpool.Pool, label string) (platformUID, userID string) {
	t.Helper()
	platformUID = uniqueUID(label)
	userID = registerUser(t, h, pool, platformUID)
	setClearance(t, pool, userID, pb.Clearance_CLEARANCE_REGISTERED)
	return platformUID, userID
}

func TestUpdateReminderRefusesAnotherUsersRow(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID := uniqueUID("rem-owner")
	ownerID := registerUser(t, h, pool, ownerUID)
	setClearance(t, pool, ownerID, pb.Clearance_CLEARANCE_REGISTERED)
	cleanupActionRecords(t, pool, ownerID)

	attackerUID := uniqueUID("rem-attacker")
	attackerID := registerUser(t, h, pool, attackerUID)
	setClearance(t, pool, attackerID, pb.Clearance_CLEARANCE_REGISTERED)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("upd"), "")

	// The request must be otherwise valid so it reaches the ownership check.
	future := time.Now().Add(2 * time.Hour).UTC()
	newMsg := "hijacked"
	tz := "Europe/Helsinki"
	_, err := h.Reminder.UpdateReminder(
		callerCtx(pb.Platform_PLATFORM_DISCORD, attackerUID),
		pb.UpdateReminderReq_builder{
			Id:          &id,
			Datetime:    timestamppb.New(future),
			Timezone:    &tz,
			Message:     &newMsg,
			Destination: destinationFor(t, pool, uniqueUID("upd-dest")),
		}.Build(),
	)
	requireCode(t, err, connect.CodeNotFound)
}

func TestDeleteReminderRefusesAnotherUsersRow(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID := uniqueUID("del-owner")
	ownerID := registerUser(t, h, pool, ownerUID)
	setClearance(t, pool, ownerID, pb.Clearance_CLEARANCE_REGISTERED)
	cleanupActionRecords(t, pool, ownerID)

	attackerUID := uniqueUID("del-attacker")
	attackerID := registerUser(t, h, pool, attackerUID)
	setClearance(t, pool, attackerID, pb.Clearance_CLEARANCE_REGISTERED)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("del"), "")

	_, err := h.Reminder.DeleteReminder(
		callerCtx(pb.Platform_PLATFORM_DISCORD, attackerUID),
		pb.DeleteReminderReq_builder{Id: &id}.Build(),
	)
	requireCode(t, err, connect.CodeNotFound)

	got, err := h.Reminder.GetReminder(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.GetReminderReq_builder{Id: &id}.Build(),
	)
	if err != nil {
		t.Fatalf("owner GetReminder after failed foreign delete: %v", err)
	}
	if got.GetReminder().GetId() != id {
		t.Errorf("id = %q, want %q", got.GetReminder().GetId(), id)
	}
}

func TestListRemindersScopesToCaller(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID := uniqueUID("list-owner")
	ownerID := registerUser(t, h, pool, ownerUID)
	setClearance(t, pool, ownerID, pb.Clearance_CLEARANCE_REGISTERED)

	otherUID := uniqueUID("list-other")
	otherID := registerUser(t, h, pool, otherUID)
	setClearance(t, pool, otherID, pb.Clearance_CLEARANCE_REGISTERED)
	cleanupActionRecords(t, pool, ownerID)
	cleanupActionRecords(t, pool, otherID)

	mine := createReminderVia(t, h, pool, ownerUID, uniqueUID("mine"), "")
	theirs := createReminderVia(t, h, pool, otherUID, uniqueUID("theirs"), "")

	resp, err := h.Reminder.ListReminders(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.ListRemindersReq_builder{}.Build(),
	)
	if err != nil {
		t.Fatalf("ListReminders: %v", err)
	}

	seen := map[string]bool{}
	for _, r := range resp.GetReminders() {
		seen[r.GetId()] = true
	}
	if !seen[mine] {
		t.Errorf("own reminder %s missing from list", mine)
	}
	if seen[theirs] {
		t.Errorf("list leaked another user's reminder %s", theirs)
	}
}

// One create through the RPC yields a real destination; the rest are seeded as PENDING.
func TestCreateReminderRefusedAtPerUserCap(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID := uniqueUID("cap-owner")
	ownerID := registerUser(t, h, pool, ownerUID)
	setClearance(t, pool, ownerID, pb.Clearance_CLEARANCE_REGISTERED)
	cleanupActionRecords(t, pool, ownerID)

	firstID := createReminderVia(t, h, pool, ownerUID, uniqueUID("cap-seed"), "")
	var destinationID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT destination_id FROM reminder WHERE id = $1`, firstID,
	).Scan(&destinationID); err != nil {
		t.Fatalf("read destination_id: %v", err)
	}

	future := time.Now().Add(time.Hour).UTC()
	pending := int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number())
	for i := 0; i < maxActiveRemindersPerUser-1; i++ {
		rid := uuidV7(t)
		// ref has no database default for reminder (unlike trigger): it is
		// unique per user_id, so a raw seed insert must supply one itself.
		ref := int64(i + 1000)
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO reminder (id, ref, datetime, timezone, destination_id, status, user_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			rid, ref, future, "Europe/Helsinki", destinationID, pending, ownerID,
		); err != nil {
			t.Fatalf("seed reminder %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM reminder WHERE user_id = $1`, ownerID); err != nil {
			t.Errorf("cleanup reminders for %s: %v", ownerID, err)
		}
	})

	message := "over the cap"
	tz := "Europe/Helsinki"
	_, err := h.Reminder.CreateReminder(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.CreateReminderReq_builder{
			Datetime:    timestamppb.New(future),
			Timezone:    &tz,
			Message:     &message,
			Destination: destinationFor(t, pool, uniqueUID("cap-over")),
		}.Build(),
	)
	requireCode(t, err, connect.CodeFailedPrecondition)
}

func uuidV7(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id.String()
}

func TestCreateReminderWritesActionRecord(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "ar-create")
	cleanupActionRecords(t, pool, ownerID)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("arc"), "")

	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_CREATED); got != 1 {
		t.Errorf("REMINDER_CREATED action records for %s = %d, want 1 (reminder %s)", ownerID, got, id)
	}
}

func TestConfirmDeliveryFinalisesAOneShot(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "ac8-owner")
	cleanupActionRecords(t, pool, ownerID)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("ac8"), "")
	markSent(t, pool, id)
	_, dueAt, _ := reminderState(t, pool, id)

	delivered := true
	if _, err := h.Reminder.ConfirmDelivery(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.ConfirmDeliveryReq_builder{Id: &id, Delivered: &delivered}.Build(),
	); err != nil {
		t.Fatalf("ConfirmDelivery: %v", err)
	}

	status, datetime, claimedAt := reminderState(t, pool, id)
	if status != int32(pb.ReminderStatus_REMINDER_STATUS_DELIVERED.Number()) {
		t.Errorf("status = %d, want DELIVERED (%d)",
			status, pb.ReminderStatus_REMINDER_STATUS_DELIVERED.Number())
	}
	if !datetime.Equal(dueAt) {
		t.Errorf("datetime moved from %v to %v; a one-shot must not be rescheduled", dueAt, datetime)
	}
	if claimedAt != nil {
		t.Errorf("claimed_at = %v, want NULL once the reminder left SENT", claimedAt)
	}
	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 1 {
		t.Errorf("REMINDER_DELIVERED action records = %d, want 1", got)
	}
}

// Compared against an independently computed NextOccurrence, in the reminder's timezone.
func TestConfirmDeliveryRescheduledARepeatToItsNextOccurrence(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "ac9-owner")
	cleanupActionRecords(t, pool, ownerID)

	// Coarse enough that the server's clock and this test's agree on the next occurrence.
	const schedule = "0 9 * * *"
	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("ac9"), schedule)
	markSent(t, pool, id)

	loc, err := time.LoadLocation("Europe/Helsinki")
	if err != nil {
		t.Fatalf("load Europe/Helsinki: %v", err)
	}
	wantNext, err := reminder.NextOccurrence(schedule, time.Now(), loc)
	if err != nil {
		t.Fatalf("NextOccurrence: %v", err)
	}

	delivered := true
	if _, err := h.Reminder.ConfirmDelivery(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.ConfirmDeliveryReq_builder{Id: &id, Delivered: &delivered}.Build(),
	); err != nil {
		t.Fatalf("ConfirmDelivery: %v", err)
	}

	status, datetime, claimedAt := reminderState(t, pool, id)
	if status != int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()) {
		t.Errorf("status = %d, want PENDING (%d) after a repeat reschedules",
			status, pb.ReminderStatus_REMINDER_STATUS_PENDING.Number())
	}
	// datetime is stored as `timestamp without time zone` holding the UTC instant.
	gotUTC := datetime.UTC().Truncate(time.Minute)
	wantUTC := wantNext.UTC().Truncate(time.Minute)
	if !gotUTC.Equal(wantUTC) {
		t.Errorf("rescheduled datetime = %v, want the next occurrence %v", gotUTC, wantUTC)
	}
	if !datetime.After(time.Now().UTC()) {
		t.Errorf("rescheduled datetime %v is not in the future; the repeat would fire immediately", datetime)
	}
	if claimedAt != nil {
		t.Errorf("claimed_at = %v, want NULL after a reschedule", claimedAt)
	}
	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 1 {
		t.Errorf("REMINDER_DELIVERED action records = %d, want 1", got)
	}
}

func TestConfirmDeliveryFailureMarksFailedAndRecordsNothing(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "fail-owner")
	cleanupActionRecords(t, pool, ownerID)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("fail"), "")
	markSent(t, pool, id)

	delivered := false
	if _, err := h.Reminder.ConfirmDelivery(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.ConfirmDeliveryReq_builder{Id: &id, Delivered: &delivered}.Build(),
	); err != nil {
		t.Fatalf("ConfirmDelivery: %v", err)
	}

	status, _, claimedAt := reminderState(t, pool, id)
	if status != int32(pb.ReminderStatus_REMINDER_STATUS_FAILED.Number()) {
		t.Errorf("status = %d, want FAILED (%d)",
			status, pb.ReminderStatus_REMINDER_STATUS_FAILED.Number())
	}
	if claimedAt != nil {
		t.Errorf("claimed_at = %v, want NULL", claimedAt)
	}
	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 0 {
		t.Errorf("REMINDER_DELIVERED action records = %d, want 0 for a failed delivery", got)
	}
}

// Two clients confirming one delivery must not produce two DELIVERED rows.
func TestConfirmDeliveryTwiceRecordsOneDelivery(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "dup-owner")
	cleanupActionRecords(t, pool, ownerID)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("dup"), "")
	markSent(t, pool, id)

	ctx := callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID)
	delivered := true
	req := pb.ConfirmDeliveryReq_builder{Id: &id, Delivered: &delivered}.Build()

	if _, err := h.Reminder.ConfirmDelivery(ctx, req); err != nil {
		t.Fatalf("first ConfirmDelivery: %v", err)
	}
	if _, err := h.Reminder.ConfirmDelivery(ctx, req); err != nil {
		t.Fatalf("duplicate ConfirmDelivery returned an error, want OK: %v", err)
	}

	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 1 {
		t.Errorf("REMINDER_DELIVERED action records = %d, want exactly 1 for one delivery", got)
	}
}

// NotFound, not PermissionDenied: the response must not confirm the id exists.
func TestConfirmDeliveryRefusesAnotherUsersRow(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "cd-owner")
	attackerUID, attackerID := registeredCaller(t, h, pool, "cd-attacker")
	cleanupActionRecords(t, pool, ownerID)
	cleanupActionRecords(t, pool, attackerID)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("cd"), "0 9 * * *")
	markSent(t, pool, id)
	beforeStatus, beforeDatetime, _ := reminderState(t, pool, id)

	for _, delivered := range []bool{true, false} {
		t.Run(map[bool]string{true: "delivered", false: "failed"}[delivered], func(t *testing.T) {
			value := delivered
			_, err := h.Reminder.ConfirmDelivery(
				callerCtx(pb.Platform_PLATFORM_DISCORD, attackerUID),
				pb.ConfirmDeliveryReq_builder{Id: &id, Delivered: &value}.Build(),
			)
			requireCode(t, err, connect.CodeNotFound)
		})
	}

	status, datetime, _ := reminderState(t, pool, id)
	if status != beforeStatus {
		t.Errorf("status changed from %d to %d; a non-owner mutated the reminder", beforeStatus, status)
	}
	if !datetime.Equal(beforeDatetime) {
		t.Errorf("datetime changed from %v to %v; a non-owner rescheduled the reminder",
			beforeDatetime, datetime)
	}

	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 0 {
		t.Errorf("REMINDER_DELIVERED records attributed to the owner = %d, want 0", got)
	}
	if got := countActionRecords(t, pool, attackerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 0 {
		t.Errorf("REMINDER_DELIVERED records attributed to the attacker = %d, want 0", got)
	}

	delivered := true
	if _, err := h.Reminder.ConfirmDelivery(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.ConfirmDeliveryReq_builder{Id: &id, Delivered: &delivered}.Build(),
	); err != nil {
		t.Fatalf("owner ConfirmDelivery on their own reminder: %v", err)
	}
}

func TestConfirmDeliveryForAnUnknownIdIsNotFound(t *testing.T) {
	h, pool := liveReminderHarness(t)

	callerUID, cdghostID := registeredCaller(t, h, pool, "cd-ghost")
	cleanupActionRecords(t, pool, cdghostID)

	unknown := uuidV7(t)
	delivered := true
	_, err := h.Reminder.ConfirmDelivery(
		callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
		pb.ConfirmDeliveryReq_builder{Id: &unknown, Delivered: &delivered}.Build(),
	)
	requireCode(t, err, connect.CodeNotFound)
}

// An absent repeat must mean "leave it alone", not "set it to NULL".
func TestUpdateReminderKeepsAnUnsuppliedRepeat(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, keeprepeatID := registeredCaller(t, h, pool, "keep-repeat")
	cleanupActionRecords(t, pool, keeprepeatID)

	const schedule = "0 9 * * *"
	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("keep"), schedule)

	future := time.Now().Add(3 * time.Hour).UTC()
	newMsg := "only the message changed"
	tz := "Europe/Helsinki"
	if _, err := h.Reminder.UpdateReminder(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.UpdateReminderReq_builder{
			Id:          &id,
			Datetime:    timestamppb.New(future),
			Timezone:    &tz,
			Message:     &newMsg,
			Destination: destinationFor(t, pool, uniqueUID("keep-dest")),
		}.Build(),
	); err != nil {
		t.Fatalf("UpdateReminder: %v", err)
	}

	var repeatCron *string
	if err := pool.QueryRow(context.Background(),
		`SELECT repeat_cron FROM reminder WHERE id = $1`, id,
	).Scan(&repeatCron); err != nil {
		t.Fatalf("read repeat_cron: %v", err)
	}
	if repeatCron == nil {
		t.Fatal("a message-only edit destroyed the repeat schedule")
	}
	if *repeatCron != schedule {
		t.Errorf("repeat_cron = %q, want %q untouched", *repeatCron, schedule)
	}
}

func TestUpdateReminderKeepsAnUnsuppliedMessage(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, keepmsgID := registeredCaller(t, h, pool, "keep-message")
	cleanupActionRecords(t, pool, keepmsgID)

	suffix := uniqueUID("keepmsg")
	id := createReminderVia(t, h, pool, ownerUID, suffix, "")
	seeded := "reminder-" + suffix

	future := time.Now().Add(3 * time.Hour).UTC()
	tz := "Europe/Helsinki"
	// No Message field: a datetime-only edit must not NULL the stored text.
	if _, err := h.Reminder.UpdateReminder(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.UpdateReminderReq_builder{
			Id:          &id,
			Datetime:    timestamppb.New(future),
			Timezone:    &tz,
			Destination: destinationFor(t, pool, uniqueUID("keepmsg-dest")),
		}.Build(),
	); err != nil {
		t.Fatalf("UpdateReminder: %v", err)
	}

	var message *string
	if err := pool.QueryRow(context.Background(),
		`SELECT message FROM reminder WHERE id = $1`, id,
	).Scan(&message); err != nil {
		t.Fatalf("read message: %v", err)
	}
	if message == nil {
		t.Fatal("a datetime-only edit destroyed the message")
	}
	if *message != seeded {
		t.Errorf("message = %q, want %q untouched", *message, seeded)
	}
}

// The clear sentinel is an explicitly set empty repeat_cron, which validation must admit.
func TestUpdateReminderClearsTheRepeatWithTheEmptySentinel(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, clearrepeatID := registeredCaller(t, h, pool, "clear-repeat")
	cleanupActionRecords(t, pool, clearrepeatID)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("clear"), "0 9 * * *")

	future := time.Now().Add(3 * time.Hour).UTC()
	msg := "no longer repeating"
	tz := "Europe/Helsinki"
	cleared := ""
	if _, err := h.Reminder.UpdateReminder(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.UpdateReminderReq_builder{
			Id:          &id,
			Datetime:    timestamppb.New(future),
			Timezone:    &tz,
			Message:     &msg,
			RepeatCron:  &cleared,
			Destination: destinationFor(t, pool, uniqueUID("clear-dest")),
		}.Build(),
	); err != nil {
		t.Fatalf("UpdateReminder with the clear sentinel: %v", err)
	}

	var repeatCron *string
	if err := pool.QueryRow(context.Background(),
		`SELECT repeat_cron FROM reminder WHERE id = $1`, id,
	).Scan(&repeatCron); err != nil {
		t.Fatalf("read repeat_cron: %v", err)
	}
	if repeatCron != nil {
		t.Errorf("repeat_cron = %q, want NULL after the clear sentinel", *repeatCron)
	}
}

// The destination is a channel, so the floor is 12 hours and an hourly repeat is refused.
func TestUpdateReminderRejectsATooFrequentRepeat(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, floorID := registeredCaller(t, h, pool, "floor")
	cleanupActionRecords(t, pool, floorID)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("floor"), "0 9 * * *")

	future := time.Now().Add(3 * time.Hour).UTC()
	msg := "too often"
	tz := "Europe/Helsinki"
	hourly := "0 * * * *"
	_, err := h.Reminder.UpdateReminder(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.UpdateReminderReq_builder{
			Id:          &id,
			Datetime:    timestamppb.New(future),
			Timezone:    &tz,
			Message:     &msg,
			RepeatCron:  &hourly,
			Destination: destinationFor(t, pool, uniqueUID("floor-dest")),
		}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestListRemindersCarriesDestinations(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, listdestID := registeredCaller(t, h, pool, "list-dest")
	cleanupActionRecords(t, pool, listdestID)

	suffix := uniqueUID("ld")
	id := createReminderVia(t, h, pool, ownerUID, suffix, "")
	wantUID := originFor(suffix).DestinationUID

	resp, err := h.Reminder.ListReminders(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.ListRemindersReq_builder{}.Build(),
	)
	if err != nil {
		t.Fatalf("ListReminders: %v", err)
	}

	for _, r := range resp.GetReminders() {
		if r.GetId() != id {
			continue
		}
		destination := r.GetDestination()
		if destination == nil {
			t.Fatal("listed reminder carries no destination")
		}
		if got := destination.GetPlatformEnum(); got != pb.Platform_PLATFORM_DISCORD {
			t.Errorf("destination platform = %v, want PLATFORM_DISCORD", got)
		}
		got := destination.GetDestinationMeta().GetFields()[callermeta.FieldDestinationUID].GetStringValue()
		if got != wantUID {
			t.Errorf("destination uid = %q, want %q", got, wantUID)
		}
		return
	}
	t.Fatalf("reminder %s missing from the listing", id)
}

// Two users, because a single-user fixture cannot tell "the caller" from "whoever".
func TestCreateActionRecordAttributesTheCallerAndNobodyElse(t *testing.T) {
	h, pool := liveReminderHarness(t)

	callerPlatformUID, callerID := registeredCaller(t, h, pool, "ar-caller")
	_, bystanderID := registeredCaller(t, h, pool, "ar-bystander")
	cleanupActionRecords(t, pool, callerID)
	cleanupActionRecords(t, pool, bystanderID)

	actionType := pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED
	if _, err := h.Analytics.CreateActionRecord(
		callerCtx(pb.Platform_PLATFORM_DISCORD, callerPlatformUID),
		pb.CreateActionRecordReq_builder{ActionType: &actionType}.Build(),
	); err != nil {
		t.Fatalf("CreateActionRecord: %v", err)
	}

	if got := countActionRecords(t, pool, callerID, actionType); got != 1 {
		t.Errorf("records attributed to the caller = %d, want 1", got)
	}
	if got := countActionRecords(t, pool, bystanderID, actionType); got != 0 {
		t.Errorf("records attributed to the bystander = %d, want 0; the write is not scoped to the caller", got)
	}
}
