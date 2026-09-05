//go:build integration

package server

import (
	"errors"
	"strconv"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// requireIdenticalNotFound is the crux of the security property: a ref that
// does not exist and a ref that exists but is not this caller's business must
// be indistinguishable, or resolution itself becomes an enumeration oracle.
func requireIdenticalNotFound(t *testing.T, nonexistentErr, foreignErr error) {
	t.Helper()

	requireCode(t, nonexistentErr, connect.CodeNotFound)
	requireCode(t, foreignErr, connect.CodeNotFound)

	var nonexistentConn, foreignConn *connect.Error
	if !errors.As(nonexistentErr, &nonexistentConn) {
		t.Fatalf("nonexistent-ref error is not a *connect.Error: %v", nonexistentErr)
	}
	if !errors.As(foreignErr, &foreignConn) {
		t.Fatalf("foreign-ref error is not a *connect.Error: %v", foreignErr)
	}

	if nonexistentConn.Message() != foreignConn.Message() {
		t.Errorf("messages differ: nonexistent ref = %q, foreign ref = %q; "+
			"a caller could tell the two apart and enumerate refs", nonexistentConn.Message(), foreignConn.Message())
	}
}

// unallocatedRef is large enough that no test run could plausibly have
// allocated it, for either the global trigger sequence or a per-user reminder
// counter.
const unallocatedRef = 9_000_000_000_000

// TestGetTriggerByRefNonexistentAndCrossInstanceYieldIdenticalNotFound covers
// C.4 for triggers: a caller resolving a ref for a trigger scoped to another
// instance must see exactly what they would see for a ref nobody has ever
// been allocated.
func TestGetTriggerByRefNonexistentAndCrossInstanceYieldIdenticalNotFound(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "refsec-owner")
	strangerUID, _ := registeredCaller(t, h, pool, "refsec-stranger")
	suffix := uniqueUID("refsec")

	ownerOrigin := callermeta.Origin{InstanceUID: "refsec-owner-instance-" + suffix, DestinationUID: "refsec-owner-dest-" + suffix}
	strangerOrigin := callermeta.Origin{InstanceUID: "refsec-stranger-instance-" + suffix, DestinationUID: "refsec-stranger-dest-" + suffix}
	cleanupInstanceRows(t, pool, ownerOrigin.InstanceMeta())
	cleanupInstanceRows(t, pool, strangerOrigin.InstanceMeta())

	ownerCtx := triggerCtx(ownerUID, ownerOrigin)
	strangerCtx := triggerCtx(strangerUID, strangerOrigin)
	bootstrapInstance(t, h, strangerCtx)

	phrase := "refsec-phrase-" + suffix
	id := createTriggerVia(t, h, pool, ownerCtx, phrase, "refsec-reply", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	var ref int64
	if err := pool.QueryRow(t.Context(), `SELECT ref FROM trigger WHERE id = $1`, id).Scan(&ref); err != nil {
		t.Fatalf("read ref: %v", err)
	}
	refStr := strconv.FormatInt(ref, 10)
	nonexistentRefStr := strconv.FormatInt(unallocatedRef, 10)

	_, foreignErr := h.Trigger.GetTrigger(strangerCtx, pb.GetTriggerReq_builder{Id: &refStr}.Build())
	_, nonexistentErr := h.Trigger.GetTrigger(strangerCtx, pb.GetTriggerReq_builder{Id: &nonexistentRefStr}.Build())

	requireIdenticalNotFound(t, nonexistentErr, foreignErr)
}

// TestGetReminderByRefNonexistentAndCrossUserYieldIdenticalNotFound covers
// C.4 for reminders: GetReminderByRef scopes by the caller's own user_id in
// SQL, so a ref belonging to another user and a ref nobody has must both fail
// identically through the very same query.
func TestGetReminderByRefNonexistentAndCrossUserYieldIdenticalNotFound(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "remrefsec-owner")
	strangerUID, _ := registeredCaller(t, h, pool, "remrefsec-stranger")

	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("remrefsec"), "")

	var ref int64
	if err := pool.QueryRow(t.Context(), `SELECT ref FROM reminder WHERE id = $1`, id).Scan(&ref); err != nil {
		t.Fatalf("read ref: %v", err)
	}
	refStr := strconv.FormatInt(ref, 10)
	nonexistentRefStr := strconv.FormatInt(unallocatedRef, 10)

	strangerCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, strangerUID)

	_, foreignErr := h.Reminder.GetReminder(strangerCtx, pb.GetReminderReq_builder{Id: &refStr}.Build())
	_, nonexistentErr := h.Reminder.GetReminder(strangerCtx, pb.GetReminderReq_builder{Id: &nonexistentRefStr}.Build())

	requireIdenticalNotFound(t, nonexistentErr, foreignErr)
}

// TestResolvingATriggerRefAndItsUUIDReachTheSameRow: a decimal ref
// and the canonical uuid must resolve to the same trigger.
func TestResolvingATriggerRefAndItsUUIDReachTheSameRow(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "refuuid-owner")
	suffix := uniqueUID("refuuid")

	origin := callermeta.Origin{InstanceUID: "refuuid-instance-" + suffix, DestinationUID: "refuuid-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	phrase := "refuuid-phrase-" + suffix
	id := createTriggerVia(t, h, pool, ctx, phrase, "refuuid-reply", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	var ref int64
	if err := pool.QueryRow(t.Context(), `SELECT ref FROM trigger WHERE id = $1`, id).Scan(&ref); err != nil {
		t.Fatalf("read ref: %v", err)
	}
	refStr := strconv.FormatInt(ref, 10)

	byUUID, err := h.Trigger.GetTrigger(ctx, pb.GetTriggerReq_builder{Id: &id}.Build())
	if err != nil {
		t.Fatalf("GetTrigger by uuid: %v", err)
	}
	byRef, err := h.Trigger.GetTrigger(ctx, pb.GetTriggerReq_builder{Id: &refStr}.Build())
	if err != nil {
		t.Fatalf("GetTrigger by ref: %v", err)
	}

	if byUUID.GetTrigger().GetId() != byRef.GetTrigger().GetId() {
		t.Errorf("uuid lookup resolved %q, ref lookup resolved %q, want the same row",
			byUUID.GetTrigger().GetId(), byRef.GetTrigger().GetId())
	}
	if byRef.GetTrigger().GetRef() != ref {
		t.Errorf("GetRef() = %d, want %d", byRef.GetTrigger().GetRef(), ref)
	}
}

// TestResolvingAReminderRefAndItsUUIDReachTheSameRow mirrors the trigger case.
func TestResolvingAReminderRefAndItsUUIDReachTheSameRow(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "remrefuuid-owner")
	id := createReminderVia(t, h, pool, ownerUID, uniqueUID("remrefuuid"), "")

	var ref int64
	if err := pool.QueryRow(t.Context(), `SELECT ref FROM reminder WHERE id = $1`, id).Scan(&ref); err != nil {
		t.Fatalf("read ref: %v", err)
	}
	refStr := strconv.FormatInt(ref, 10)

	ctx := callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID)

	byUUID, err := h.Reminder.GetReminder(ctx, pb.GetReminderReq_builder{Id: &id}.Build())
	if err != nil {
		t.Fatalf("GetReminder by uuid: %v", err)
	}
	byRef, err := h.Reminder.GetReminder(ctx, pb.GetReminderReq_builder{Id: &refStr}.Build())
	if err != nil {
		t.Fatalf("GetReminder by ref: %v", err)
	}

	if byUUID.GetReminder().GetId() != byRef.GetReminder().GetId() {
		t.Errorf("uuid lookup resolved %q, ref lookup resolved %q, want the same row",
			byUUID.GetReminder().GetId(), byRef.GetReminder().GetId())
	}
	if byRef.GetReminder().GetRef() != ref {
		t.Errorf("GetRef() = %d, want %d", byRef.GetReminder().GetRef(), ref)
	}
}
