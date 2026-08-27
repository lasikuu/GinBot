//go:build integration

// Integration tests for the reminder surface, driven through the harness
// with the real interceptor chain and a real database.
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/grpc/server/...
//
// These reuse the harness helpers from harness_test.go (callerCtx, requireCode,
// directory, testUser) and the live helpers from user_integration_test.go
// (requireDatabase, liveHarness, registerUser, setClearance, uniqueUID,
// cleanupUser) — none are redeclared here.
package server

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/reminder"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// liveReminderHarness wires the caller resolver AND the origin resolver to the
// real database, because CreateReminder bootstraps a destination through the
// origin interceptor. db.GetOrCreateDestinationByMeta satisfies
// interceptor.OriginResolver, so origin bootstrap writes real rows the way
// production does.
func liveReminderHarness(t *testing.T) (*harness, *pgxpool.Pool) {
	t.Helper()
	pool := requireDatabase(t)
	return newHarness(t,
		withResolver(db.GetUserByPlatformUID),
		withOriginResolver(db.GetOrCreateDestinationByMeta),
	), pool
}

// withOriginResolver injects a real interceptor.OriginResolver (e.g.
// db.GetOrCreateDestinationByMeta). The shared harness defaults to the
// in-memory originLog fake, which writes no rows; this option is the
// integration equivalent and sets resolveOrigin directly on the harness config.
func withOriginResolver(resolve interceptor.OriginResolver) harnessOption {
	return func(cfg *harnessConfig) { cfg.resolveOrigin = resolve }
}

// originFor builds a unique instance/destination identity for a reminder.
//
// The jsonb shapes come from callermeta.Origin, which is what production writes.
// Hand-spelled fixtures ({"guild_id": …}) do not match the keys the delivery path
// reads, and they also make every destination look like a DM to
// isDMDestination — so the repeat-interval floor under test would be the wrong
// one.
func originFor(suffix string) callermeta.Origin {
	return callermeta.Origin{
		InstanceUID:    "instance-" + suffix,
		DestinationUID: "destination-" + suffix,
	}
}

// destinationFor builds a unique Discord destination for a reminder create, and
// schedules cleanup of the instance and destination rows it will cause.
//
// CreateReminder resolves a destination through db.GetOrCreateDestinationByMeta,
// which inserts an instance and a destination as a side effect. Cleaning up only
// the reminder leaked both of those on every call — 19 instance and 19
// destination rows per suite run. The pool is threaded in rather than left to
// each caller so the cleanup cannot be forgotten at a new call site.
func destinationFor(t *testing.T, pool *pgxpool.Pool, suffix string) *pb.ReminderDestination {
	t.Helper()
	origin := originFor(suffix)
	instanceMeta := origin.InstanceMeta()

	// LIFO: registered before the reminder's own cleanup, so it runs after it.
	// destination is referenced by reminder.destination_id with NO ACTION, so
	// the children must go first.
	cleanupInstanceRows(t, pool, instanceMeta)

	return pb.ReminderDestination_builder{
		PlatformEnum:    pb.Platform_PLATFORM_DISCORD.Enum(),
		InstanceMeta:    instanceMeta,
		DestinationMeta: origin.DestinationMeta(),
	}.Build()
}

// cleanupInstanceRows removes the reminders, destination and instance created
// for a fixture, innermost first. Every error is asserted: a silently discarded
// cleanup is how the leak above went unnoticed.
//
// Reminders are deleted here as well as by their own cleanup, because
// t.Cleanup is LIFO and a test that repoints a reminder at a SECOND destination
// registers that destination's cleanup last — so it would otherwise run while
// the reminder still references it, and fk_reminder_destination is NO ACTION.
// Deleting the referencing rows first makes this independent of ordering.
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

// createReminderVia creates a reminder through the public CreateReminder RPC as
// the given caller, returning its id, and schedules cleanup of the row.
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

// markSent makes a reminder due and puts it in SENT with a claim stamp, exactly
// as db.ClaimDueReminders would, so a ConfirmDelivery has something to advance.
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

// reminderState reads back the fields the delivery state machine moves.
func reminderState(t *testing.T, pool *pgxpool.Pool, id string) (status int32, datetime time.Time, claimedAt *time.Time) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT status, datetime, claimed_at FROM reminder WHERE id = $1`, id,
	).Scan(&status, &datetime, &claimedAt); err != nil {
		t.Fatalf("read reminder %s: %v", id, err)
	}
	return status, datetime, claimedAt
}

// countActionRecords counts one actor's records of one type.
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

// cleanupActionRecords removes an actor's analytics rows.
func cleanupActionRecords(t *testing.T, pool *pgxpool.Pool, actorID string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM action_record WHERE actor_id = $1`, actorID); err != nil {
			t.Errorf("cleanup action_record for %s: %v", actorID, err)
		}
	})
}

// registeredCaller creates a REGISTERED user and returns its platform uid and
// account id — the two identities every reminder test needs.
func registeredCaller(t *testing.T, h *harness, pool *pgxpool.Pool, label string) (platformUID, userID string) {
	t.Helper()
	platformUID = uniqueUID(label)
	userID = registerUser(t, h, pool, platformUID)
	setClearance(t, pool, userID, pb.Clearance_CLEARANCE_REGISTERED)
	return platformUID, userID
}

// TestUpdateReminderRefusesAnotherUsersRow: through the RPC, a caller who does
// not own the reminder gets NotFound (the GetReminder privacy pattern).
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

	// The update request must be otherwise valid (future datetime, timezone,
	// destination) so it passes the validation interceptor and the handler's
	// required-field checks, reaching the ownership check — which must reject a
	// non-owner with NotFound (GetReminder privacy pattern), not tell them the
	// reminder exists.
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
	requireCode(t, err, codes.NotFound)
}

// TestDeleteReminderRefusesAnotherUsersRow: same privacy boundary for delete.
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
	requireCode(t, err, codes.NotFound)

	// The owner can still see it.
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

// TestListRemindersScopesToCaller: List returns only the caller's reminders.
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

// TestCreateReminderRefusedAtPerUserCap: creating past the 100 active-reminder
// cap is refused with FailedPrecondition. One reminder is created through the
// RPC to obtain a real destination, then the remaining rows up to the cap are
// seeded directly (cheaply) as PENDING, then a final create must be rejected.
func TestCreateReminderRefusedAtPerUserCap(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID := uniqueUID("cap-owner")
	ownerID := registerUser(t, h, pool, ownerUID)
	setClearance(t, pool, ownerID, pb.Clearance_CLEARANCE_REGISTERED)
	cleanupActionRecords(t, pool, ownerID)

	// First real create yields a destination we can reuse for the bulk seed.
	firstID := createReminderVia(t, h, pool, ownerUID, uniqueUID("cap-seed"), "")
	var destinationID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT destination_id FROM reminder WHERE id = $1`, firstID,
	).Scan(&destinationID); err != nil {
		t.Fatalf("read destination_id: %v", err)
	}

	// Seed up to the cap: we already have 1, add (cap-1) more PENDING rows.
	future := time.Now().Add(time.Hour).UTC()
	pending := int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number())
	for i := 0; i < maxActiveRemindersPerUser-1; i++ {
		rid := uuidV7(t)
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO reminder (id, datetime, timezone, destination_id, status, user_id)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			rid, future, "Europe/Helsinki", destinationID, pending, ownerID,
		); err != nil {
			t.Fatalf("seed reminder %d: %v", i, err)
		}
	}
	// Bulk cleanup for the whole user's reminders (covers the seeded rows).
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM reminder WHERE user_id = $1`, ownerID); err != nil {
			t.Errorf("cleanup reminders for %s: %v", ownerID, err)
		}
	})

	// The next create is the 101st active — must be refused.
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
	requireCode(t, err, codes.FailedPrecondition)
}

func uuidV7(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id.String()
}

// TestCreateReminderWritesActionRecord: creating a reminder writes an
// action_record of type REMINDER_CREATED with the creator as actor.
func TestCreateReminderWritesActionRecord(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "ar-create")
	cleanupActionRecords(t, pool, ownerID)

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("arc"), "")

	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_CREATED); got != 1 {
		t.Errorf("REMINDER_CREATED action records for %s = %d, want 1 (reminder %s)", ownerID, got, id)
	}
}

// TestConfirmDeliveryFinalisesAOneShot is AC8, driven through the real RPC.
//
// The db-level version of this test used to call a helper the TEST implemented —
// it decided one-shot vs repeat and delivered vs failed itself — so it verified
// the harness and would have passed against any server. What matters is that
// ReminderServer.ConfirmDelivery makes those decisions, so the assertions are on
// the RPC's effect: a one-shot ends DELIVERED, keeps its fire time, releases its
// claim, and is recorded once.
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
	// A one-shot is kept, not rescheduled and not deleted.
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

// TestConfirmDeliveryRescheduledARepeatToItsNextOccurrence is AC9, through the
// real RPC.
//
// The new fire time is compared against reminder.NextOccurrence computed
// INDEPENDENTLY here, rather than against an arbitrary "next" the test passes
// in — the point of the criterion is that the server advances the schedule
// correctly, in the reminder's own timezone.
func TestConfirmDeliveryRescheduledARepeatToItsNextOccurrence(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "ac9-owner")
	cleanupActionRecords(t, pool, ownerID)

	// Daily at 09:00. Well above the 12h channel floor, and coarse enough that
	// the server's time.Now() and this test's agree on the next occurrence.
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

// TestConfirmDeliveryFailureMarksFailedAndRecordsNothing: delivered=false is a
// terminal failure, and it is NOT a delivery, so no REMINDER_DELIVERED row.
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

// TestConfirmDeliveryTwiceRecordsOneDelivery is the MUST-FIX regression test for
// the analytics write.
//
// Both AdvanceReminderStatusIfSent and RescheduleReminderIfSent report whether a
// row actually moved, and the analytics write must be conditional on that. It
// used to be unconditional, so two ginbot-discord instances during a rolling
// restart — both receiving the fan-out, both posting, both confirming — produced
// two rows for one delivery, making the "reminders delivered" metric count
// confirmations instead.
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
	// The duplicate is a no-op, not an error: the client did its job.
	if _, err := h.Reminder.ConfirmDelivery(ctx, req); err != nil {
		t.Fatalf("duplicate ConfirmDelivery returned an error, want OK: %v", err)
	}

	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 1 {
		t.Errorf("REMINDER_DELIVERED action records = %d, want exactly 1 for one delivery", got)
	}
}

// TestConfirmDeliveryRefusesAnotherUsersRow is the ownership boundary that
// ConfirmDelivery was missing while Update and Delete both had it.
//
// It resolved the caller and threw it away, then loaded and mutated the reminder
// by id alone. Any CLEARANCE_REGISTERED caller holding an id could, while the row
// was SENT, suppress the reminder by confirming a failure, move a repeating
// reminder's schedule, use the NotFound-vs-OK split as an existence oracle, and
// have a forged REMINDER_DELIVERED attributed to the owner.
//
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
			requireCode(t, err, codes.NotFound)
		})
	}

	// The reminder is untouched: still SENT, still due at the same time.
	status, datetime, _ := reminderState(t, pool, id)
	if status != beforeStatus {
		t.Errorf("status changed from %d to %d; a non-owner mutated the reminder", beforeStatus, status)
	}
	if !datetime.Equal(beforeDatetime) {
		t.Errorf("datetime changed from %v to %v; a non-owner rescheduled the reminder",
			beforeDatetime, datetime)
	}

	// And no analytics row was forged against either party.
	if got := countActionRecords(t, pool, ownerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 0 {
		t.Errorf("REMINDER_DELIVERED records attributed to the owner = %d, want 0", got)
	}
	if got := countActionRecords(t, pool, attackerID, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED); got != 0 {
		t.Errorf("REMINDER_DELIVERED records attributed to the attacker = %d, want 0", got)
	}

	// The owner can still confirm their own reminder, so the check is an
	// ownership check and not a blanket refusal.
	delivered := true
	if _, err := h.Reminder.ConfirmDelivery(
		callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID),
		pb.ConfirmDeliveryReq_builder{Id: &id, Delivered: &delivered}.Build(),
	); err != nil {
		t.Fatalf("owner ConfirmDelivery on their own reminder: %v", err)
	}
}

// TestConfirmDeliveryForAnUnknownIdIsNotFound: the same code as a non-owner's
// attempt, so the two are indistinguishable to a caller probing for ids.
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
	requireCode(t, err, codes.NotFound)
}

// TestUpdateReminderKeepsAnUnsuppliedRepeat is AC6 end to end, through the RPC
// and the whole interceptor chain.
//
// /remindermod also changes the time and the message, so an absent repeat has to
// mean "leave it alone". It used to mean "set it to NULL", which silently turned
// a repeating reminder into a one-shot on any edit.
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

// TestUpdateReminderClearsTheRepeatWithTheEmptySentinel: a user must still be
// able to REMOVE a repeat, which needs a value distinguishable from "absent".
//
// The sentinel is an explicitly-set empty repeat_cron. This also proves the
// request survives the validation interceptor: the field's pattern has to admit
// the empty string, or the whole clear path is unreachable with InvalidArgument.
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

// TestUpdateReminderRejectsATooFrequentRepeat: an update must apply the same
// minimum-interval floor create does, or a schedule create would have refused
// could be installed by editing afterwards.
//
// The destination is a channel (its instance_uid is set), so the floor is 12
// hours and an hourly repeat is refused.
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
	requireCode(t, err, codes.InvalidArgument)
}

// TestListRemindersCarriesDestinations: the listing joins each reminder's
// destination, which is both what the client needs to render one and what
// replaced a per-row destination lookup.
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

// TestCreateActionRecordAttributesTheCallerAndNobodyElse: the record lands on
// the caller resolved from metadata, and on no other account.
//
// CreateActionRecordReq.actor_id is deleted and reserved, so asking for someone
// else is now unrepresentable and the assertion that such a request was
// IGNORED has no subject left. What is asserted instead is not the same claim
// and is still falsifiable: the handler passes caller.ID to
// db.CreateActionRecord, so a bystander account must come out with nothing.
// That is the only way the old hole could reappear now that the field cannot.
//
// Two registered users and a count on each, because a single-user fixture
// cannot tell "attributed to the caller" apart from "attributed to whichever
// account the handler happened to reach for".
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
