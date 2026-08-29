//go:build integration

package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// liveTriggerHarness wires both resolvers to the real database: scoping needs the
// origin interceptor to have bootstrapped the instance row for the call's origin.
func liveTriggerHarness(t *testing.T) (*harness, *pgxpool.Pool) {
	t.Helper()
	pool := requireDatabase(t)
	return newHarness(t,
		withResolver(db.GetUserByPlatformUID),
		withOriginResolver(db.GetOrCreateDestinationByMeta),
	), pool
}

func triggerCtx(platformUID string, origin callermeta.Origin) context.Context {
	return originCtx(callerCtx(pb.Platform_PLATFORM_DISCORD, platformUID), origin)
}

func triggerInstanceFor(origin callermeta.Origin) *pb.TriggerInstance {
	platform := pb.Platform_PLATFORM_DISCORD
	return pb.TriggerInstance_builder{PlatformEnum: &platform, InstanceMeta: origin.InstanceMeta()}.Build()
}

// cleanupTriggerRow also removes action_record rows, which carry no FK to trigger.
// Registered after the fixture's user/instance cleanup so LIFO runs it first.
func cleanupTriggerRow(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `DELETE FROM action_record WHERE subject_id = $1`, id); err != nil {
			t.Errorf("cleanup action_record for trigger %s: %v", id, err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM trigger WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup trigger %s: %v", id, err)
		}
	})
}

// createTriggerVia scopes by falling back to the caller's own origin, and cleans up.
func createTriggerVia(t *testing.T, h *harness, pool *pgxpool.Pool, ctx context.Context, phrase, reply string, chance int32, mode pb.TriggerMode) string {
	t.Helper()

	b := pb.CreateTriggerReq_builder{Phrase: &phrase, Reply: &reply, Chance: &chance}
	if mode != pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED {
		b.Mode = &mode
	}
	resp, err := h.Trigger.CreateTrigger(ctx, b.Build())
	if err != nil {
		t.Fatalf("CreateTrigger(%q): %v", phrase, err)
	}
	id := resp.GetId()
	if id == "" {
		t.Fatal("CreateTrigger returned an empty id")
	}
	cleanupTriggerRow(t, pool, id)
	return id
}

func TestTriggerCreateTryGetDeleteRoundTrip(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-rt")
	suffix := uniqueUID("rt")
	origin := callermeta.Origin{InstanceUID: "trig-rt-instance-" + suffix, DestinationUID: "trig-rt-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	phrase := "roundtrip-phrase-" + suffix
	reply := "roundtrip-reply"
	id := createTriggerVia(t, h, pool, ctx, phrase, reply, 100, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	tryResp, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{
		Instance: triggerInstanceFor(origin),
		Phrase:   &phrase,
	}.Build())
	if err != nil {
		t.Fatalf("TryTrigger: %v", err)
	}
	if tryResp.GetId() != id {
		t.Errorf("TryTrigger id = %q, want %q", tryResp.GetId(), id)
	}
	if tryResp.GetReply() != reply {
		t.Errorf("TryTrigger reply = %q, want %q", tryResp.GetReply(), reply)
	}

	getResp, err := h.Trigger.GetTrigger(ctx, pb.GetTriggerReq_builder{Id: &id}.Build())
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if getResp.GetTrigger().GetId() != id {
		t.Errorf("GetTrigger id = %q, want %q", getResp.GetTrigger().GetId(), id)
	}
	if getResp.GetTrigger().GetPhrase() != phrase {
		t.Errorf("GetTrigger phrase = %q, want %q", getResp.GetTrigger().GetPhrase(), phrase)
	}

	if _, err := h.Trigger.DeleteTrigger(ctx, pb.DeleteTriggerReq_builder{Id: &id}.Build()); err != nil {
		t.Fatalf("DeleteTrigger: %v", err)
	}

	_, err = h.Trigger.GetTrigger(ctx, pb.GetTriggerReq_builder{Id: &id}.Build())
	requireCode(t, err, connect.CodeNotFound)
}

func TestTryTriggerFiresDeterministicallyAtChance100(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-100")
	suffix := uniqueUID("chance100")
	origin := callermeta.Origin{InstanceUID: "trig-100-instance-" + suffix, DestinationUID: "trig-100-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	phrase := "always-fires-" + suffix
	reply := "always"
	id := createTriggerVia(t, h, pool, ctx, phrase, reply, 100, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	const attempts = 20
	for i := 0; i < attempts; i++ {
		resp, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{
			Instance: triggerInstanceFor(origin),
			Phrase:   &phrase,
		}.Build())
		if err != nil {
			t.Fatalf("TryTrigger attempt %d: %v", i, err)
		}
		if resp.GetId() != id {
			t.Fatalf("TryTrigger attempt %d did not fire (id=%q), want %q every time at chance 100", i, resp.GetId(), id)
		}
	}
}

func TestTriggerInstanceScopingEndToEnd(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-scope")
	suffix := uniqueUID("scope")

	originA := callermeta.Origin{InstanceUID: "scope-a-" + suffix, DestinationUID: "scope-a-dest-" + suffix}
	originB := callermeta.Origin{InstanceUID: "scope-b-" + suffix, DestinationUID: "scope-b-dest-" + suffix}
	cleanupInstanceRows(t, pool, originA.InstanceMeta())
	cleanupInstanceRows(t, pool, originB.InstanceMeta())

	ctxA := triggerCtx(ownerUID, originA)
	ctxB := triggerCtx(ownerUID, originB)

	// Bootstrap instance B for real, so the NotFound below is provably about scoping.
	if _, err := h.Trigger.ListTriggers(ctxB, pb.ListTriggersReq_builder{}.Build()); err != nil {
		t.Fatalf("bootstrap instance B via ListTriggers: %v", err)
	}

	phrase := "scope-phrase-" + suffix
	reply := "scope-reply"
	id := createTriggerVia(t, h, pool, ctxA, phrase, reply, 100, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	tryRespB, err := h.Trigger.TryTrigger(ctxB, pb.TryTriggerReq_builder{
		Instance: triggerInstanceFor(originB),
		Phrase:   &phrase,
	}.Build())
	if err != nil {
		t.Fatalf("TryTrigger on instance B: %v", err)
	}
	if tryRespB.GetId() != "" {
		t.Errorf("trigger %s scoped to instance A fired via instance B (TryTrigger)", id)
	}

	_, err = h.Trigger.ExecTrigger(ctxB, pb.ExecTriggerReq_builder{
		Id:       &id,
		Instance: triggerInstanceFor(originB),
	}.Build())
	requireCode(t, err, connect.CodeNotFound)

	execRespA, err := h.Trigger.ExecTrigger(ctxA, pb.ExecTriggerReq_builder{
		Id:       &id,
		Instance: triggerInstanceFor(originA),
	}.Build())
	if err != nil {
		t.Fatalf("ExecTrigger on instance A: %v", err)
	}
	if execRespA.GetId() != id {
		t.Errorf("ExecTrigger on instance A id = %q, want %q", execRespA.GetId(), id)
	}
}

func TestDeleteAndUpdateTriggerRefuseAnotherUsersRowWithNotFound(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-owner")
	attackerUID, _ := registeredCaller(t, h, pool, "trig-attacker")
	suffix := uniqueUID("ownership")

	origin := callermeta.Origin{InstanceUID: "ownership-instance-" + suffix, DestinationUID: "ownership-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	ownerCtx := triggerCtx(ownerUID, origin)
	attackerCtx := triggerCtx(attackerUID, origin)

	phrase := "ownership-phrase-" + suffix
	reply := "original reply"
	id := createTriggerVia(t, h, pool, ownerCtx, phrase, reply, 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	newReply := "hijacked"
	_, err := h.Trigger.UpdateTrigger(attackerCtx, pb.UpdateTriggerReq_builder{Id: &id, Reply: &newReply}.Build())
	requireCode(t, err, connect.CodeNotFound)

	_, err = h.Trigger.DeleteTrigger(attackerCtx, pb.DeleteTriggerReq_builder{Id: &id}.Build())
	requireCode(t, err, connect.CodeNotFound)

	getResp, err := h.Trigger.GetTrigger(ownerCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
	if err != nil {
		t.Fatalf("owner GetTrigger after a foreign attack attempt: %v", err)
	}
	if getResp.GetTrigger().GetReply() != reply {
		t.Errorf("reply = %q, want the original %q; the attacker's update must not have applied", getResp.GetTrigger().GetReply(), reply)
	}

	if _, err := h.Trigger.DeleteTrigger(ownerCtx, pb.DeleteTriggerReq_builder{Id: &id}.Build()); err != nil {
		t.Fatalf("owner's own DeleteTrigger: %v", err)
	}
}

func TestCreateTriggerDuplicateExactPhraseReturnsAlreadyExists(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-exact")
	suffix := uniqueUID("exact")

	origin := callermeta.Origin{InstanceUID: "exact-instance-" + suffix, DestinationUID: "exact-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	phrase := "exact-dup-" + suffix
	reply := "reply"
	createTriggerVia(t, h, pool, ctx, phrase, reply, 10, pb.TriggerMode_TRIGGER_MODE_EXACT)

	mode := pb.TriggerMode_TRIGGER_MODE_EXACT
	chance := int32(10)
	_, err := h.Trigger.CreateTrigger(ctx, pb.CreateTriggerReq_builder{
		Phrase: &phrase, Reply: &reply, Chance: &chance, Mode: &mode,
	}.Build())
	requireCode(t, err, connect.CodeAlreadyExists)
}

func TestExecTriggerRecordsTriggerCalledActionRecord(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-exec")
	suffix := uniqueUID("exec")

	origin := callermeta.Origin{InstanceUID: "exec-instance-" + suffix, DestinationUID: "exec-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	phrase := "exec-phrase-" + suffix
	reply := "exec-reply"
	id := createTriggerVia(t, h, pool, ctx, phrase, reply, 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	execResp, err := h.Trigger.ExecTrigger(ctx, pb.ExecTriggerReq_builder{
		Id:       &id,
		Instance: triggerInstanceFor(origin),
	}.Build())
	if err != nil {
		t.Fatalf("ExecTrigger: %v", err)
	}
	if execResp.GetId() != id {
		t.Errorf("ExecTrigger id = %q, want %q", execResp.GetId(), id)
	}
	if execResp.GetReply() != reply {
		t.Errorf("ExecTrigger reply = %q, want %q", execResp.GetReply(), reply)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM action_record WHERE subject_id = $1 AND action_type = $2`,
		id, int32(pb.ActionType_ACTION_TYPE_TRIGGER_CALLED.Number()),
	).Scan(&count); err != nil {
		t.Fatalf("count action_record: %v", err)
	}
	if count != 1 {
		t.Errorf("ACTION_TYPE_TRIGGER_CALLED records for trigger %s = %d, want 1", id, count)
	}
}

func TestGetTriggerStatsReturnsLeaderboardAfterSeedingFires(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-stats")
	suffix := uniqueUID("stats")

	origin := callermeta.Origin{InstanceUID: "stats-instance-" + suffix, DestinationUID: "stats-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	phrase := "stats-phrase-" + suffix
	reply := "stats-reply"
	id := createTriggerVia(t, h, pool, ctx, phrase, reply, 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	const fires = 3
	for i := 0; i < fires; i++ {
		if _, err := h.Trigger.ExecTrigger(ctx, pb.ExecTriggerReq_builder{
			Id:       &id,
			Instance: triggerInstanceFor(origin),
		}.Build()); err != nil {
			t.Fatalf("ExecTrigger seed #%d: %v", i, err)
		}
	}

	actionType := pb.ActionType_ACTION_TYPE_TRIGGER_CALLED
	statsResp, err := h.Trigger.GetTriggerStats(ctx, pb.GetTriggerStatsReq_builder{
		Instance:   triggerInstanceFor(origin),
		ActionType: &actionType,
	}.Build())
	if err != nil {
		t.Fatalf("GetTriggerStats: %v", err)
	}

	found := false
	for _, stat := range statsResp.GetStats() {
		if stat.GetTriggerId() != id {
			continue
		}
		found = true
		if stat.GetCount() != fires {
			t.Errorf("count = %d, want %d", stat.GetCount(), fires)
		}
		if stat.GetPhrase() != phrase {
			t.Errorf("phrase = %q, want %q", stat.GetPhrase(), phrase)
		}
	}
	if !found {
		t.Errorf("trigger %s missing from the leaderboard: %+v", id, statsResp.GetStats())
	}
}

// bootstrapInstance creates ctx's origin instance for real, so a later NotFound is
// provably about scoping.
func bootstrapInstance(t *testing.T, h *harness, ctx context.Context) {
	t.Helper()
	if _, err := h.Trigger.ListTriggers(ctx, pb.ListTriggersReq_builder{}.Build()); err != nil {
		t.Fatalf("bootstrap instance via ListTriggers: %v", err)
	}
}

// Chance 100 makes a cross-instance leak observable rather than theoretical.
func TestTryTriggerNamingAnotherInstanceReturnsNotFound(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-cross-try")
	suffix := uniqueUID("crosstry")

	originA := callermeta.Origin{InstanceUID: "cross-try-a-" + suffix, DestinationUID: "cross-try-a-dest-" + suffix}
	originB := callermeta.Origin{InstanceUID: "cross-try-b-" + suffix, DestinationUID: "cross-try-b-dest-" + suffix}
	cleanupInstanceRows(t, pool, originA.InstanceMeta())
	cleanupInstanceRows(t, pool, originB.InstanceMeta())

	ctxA := triggerCtx(ownerUID, originA)
	ctxB := triggerCtx(ownerUID, originB)
	bootstrapInstance(t, h, ctxA)

	phrase := "cross-try-phrase-" + suffix
	reply := "leaked-reply"
	createTriggerVia(t, h, pool, ctxB, phrase, reply, 100, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	_, err := h.Trigger.TryTrigger(ctxA, pb.TryTriggerReq_builder{
		Instance: triggerInstanceFor(originB),
		Phrase:   &phrase,
	}.Build())
	requireCode(t, err, connect.CodeNotFound)
}

// The trigger genuinely exists on B, so the NotFound can only be about the origin.
func TestExecTriggerNamingAnotherInstanceReturnsNotFound(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-cross-exec")
	suffix := uniqueUID("crossexec")

	originA := callermeta.Origin{InstanceUID: "cross-exec-a-" + suffix, DestinationUID: "cross-exec-a-dest-" + suffix}
	originB := callermeta.Origin{InstanceUID: "cross-exec-b-" + suffix, DestinationUID: "cross-exec-b-dest-" + suffix}
	cleanupInstanceRows(t, pool, originA.InstanceMeta())
	cleanupInstanceRows(t, pool, originB.InstanceMeta())

	ctxA := triggerCtx(ownerUID, originA)
	ctxB := triggerCtx(ownerUID, originB)
	bootstrapInstance(t, h, ctxA)

	phrase := "cross-exec-phrase-" + suffix
	reply := "cross-exec-reply"
	id := createTriggerVia(t, h, pool, ctxB, phrase, reply, 100, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	_, err := h.Trigger.ExecTrigger(ctxA, pb.ExecTriggerReq_builder{
		Id:       &id,
		Instance: triggerInstanceFor(originB),
	}.Build())
	requireCode(t, err, connect.CodeNotFound)
}

// A recorded fire on B is seeded first, so a leak returns a non-empty leaderboard.
func TestGetTriggerStatsNamingAnotherInstanceReturnsNotFound(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-cross-stats")
	suffix := uniqueUID("crossstats")

	originA := callermeta.Origin{InstanceUID: "cross-stats-a-" + suffix, DestinationUID: "cross-stats-a-dest-" + suffix}
	originB := callermeta.Origin{InstanceUID: "cross-stats-b-" + suffix, DestinationUID: "cross-stats-b-dest-" + suffix}
	cleanupInstanceRows(t, pool, originA.InstanceMeta())
	cleanupInstanceRows(t, pool, originB.InstanceMeta())

	ctxA := triggerCtx(ownerUID, originA)
	ctxB := triggerCtx(ownerUID, originB)
	bootstrapInstance(t, h, ctxA)

	phrase := "cross-stats-phrase-" + suffix
	reply := "cross-stats-reply"
	id := createTriggerVia(t, h, pool, ctxB, phrase, reply, 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	if _, err := h.Trigger.ExecTrigger(ctxB, pb.ExecTriggerReq_builder{
		Id:       &id,
		Instance: triggerInstanceFor(originB),
	}.Build()); err != nil {
		t.Fatalf("seed fire on instance B: %v", err)
	}

	_, err := h.Trigger.GetTriggerStats(ctxA, pb.GetTriggerStatsReq_builder{
		Instance: triggerInstanceFor(originB),
	}.Build())
	requireCode(t, err, connect.CodeNotFound)
}

func TestCreateTriggerNamingAnotherInstanceIsRefusedAndCreatesNothing(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	callerUID, _ := registeredCaller(t, h, pool, "trig-cross-create")
	suffix := uniqueUID("crosscreate")

	originA := callermeta.Origin{InstanceUID: "cross-create-a-" + suffix, DestinationUID: "cross-create-a-dest-" + suffix}
	originB := callermeta.Origin{InstanceUID: "cross-create-b-" + suffix, DestinationUID: "cross-create-b-dest-" + suffix}
	cleanupInstanceRows(t, pool, originA.InstanceMeta())
	cleanupInstanceRows(t, pool, originB.InstanceMeta())

	ctxA := triggerCtx(callerUID, originA)
	ctxB := triggerCtx(callerUID, originB)
	bootstrapInstance(t, h, ctxB)

	phrase := "cross-create-phrase-" + suffix
	reply := "reply"
	chance := int32(10)
	_, err := h.Trigger.CreateTrigger(ctxA, pb.CreateTriggerReq_builder{
		Phrase:    &phrase,
		Reply:     &reply,
		Chance:    &chance,
		Instances: []*pb.TriggerInstance{triggerInstanceFor(originB)},
	}.Build())
	requireCode(t, err, connect.CodeNotFound)

	instanceB, err := db.GetInstanceByMeta(context.Background(), pb.Platform_PLATFORM_DISCORD, originB.InstanceMeta())
	if err != nil {
		t.Fatalf("GetInstanceByMeta(B): %v", err)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM trigger_instance
		 JOIN trigger ON trigger.id = trigger_instance.trigger_id
		 WHERE trigger_instance.instance_id = $1 AND trigger.phrase = $2`,
		instanceB.ID, phrase,
	).Scan(&count); err != nil {
		t.Fatalf("count trigger_instance rows scoped to instance B: %v", err)
	}
	if count != 0 {
		t.Errorf("trigger_instance rows scoped to instance B for phrase %q = %d, want 0; the refused create must not have left a partial row behind", phrase, count)
	}
}

func TestCreateTriggerNamingOwnOriginInstanceExplicitlySucceeds(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	callerUID, _ := registeredCaller(t, h, pool, "trig-explicit-own")
	suffix := uniqueUID("explicitown")

	origin := callermeta.Origin{InstanceUID: "explicit-own-" + suffix, DestinationUID: "explicit-own-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(callerUID, origin)
	bootstrapInstance(t, h, ctx)

	phrase := "explicit-own-phrase-" + suffix
	reply := "reply"
	chance := int32(10)
	resp, err := h.Trigger.CreateTrigger(ctx, pb.CreateTriggerReq_builder{
		Phrase:    &phrase,
		Reply:     &reply,
		Chance:    &chance,
		Instances: []*pb.TriggerInstance{triggerInstanceFor(origin)},
	}.Build())
	if err != nil {
		t.Fatalf("CreateTrigger naming the caller's own origin instance explicitly: %v", err)
	}
	id := resp.GetId()
	if id == "" {
		t.Fatal("CreateTrigger returned an empty id")
	}
	cleanupTriggerRow(t, pool, id)

	getResp, err := h.Trigger.GetTrigger(ctx, pb.GetTriggerReq_builder{Id: &id}.Build())
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}

	found := false
	for _, inst := range getResp.GetTrigger().GetInstances() {
		if inst.GetInstanceMeta().GetFields()[callermeta.FieldInstanceUID].GetStringValue() == origin.InstanceUID {
			found = true
		}
	}
	if !found {
		t.Errorf("trigger %s instances = %+v, want it scoped to %q", id, getResp.GetTrigger().GetInstances(), origin.InstanceUID)
	}
}

// With no resolvable origin and no owner predicate, db.ListTriggers skips both and
// returns every trigger in the database. The fallback must run regardless of `mine`.
func TestListTriggersWithNoOriginDoesNotLeakOtherUsersTriggers(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	victimUID, _ := registeredCaller(t, h, pool, "trig-leak-victim")
	strangerUID, strangerID := registeredCaller(t, h, pool, "trig-leak-stranger")
	suffix := uniqueUID("leak")

	origin := callermeta.Origin{InstanceUID: "leak-instance-" + suffix, DestinationUID: "leak-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	victimCtx := triggerCtx(victimUID, origin)
	phrase := "leak-phrase-" + suffix
	reply := "leak-reply"
	victimTriggerID := createTriggerVia(t, h, pool, victimCtx, phrase, reply, 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	// The stranger owns one trigger too, so a handler returning nothing would not pass.
	strangerOwnCtx := triggerCtx(strangerUID, origin)
	strangerTriggerID := createTriggerVia(t, h, pool, strangerOwnCtx,
		"leak-own-phrase-"+suffix, "leak-own-reply", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	// Caller identity only, no origin: this is what a direct message looks like.
	strangerCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, strangerUID)

	mine := true
	for _, tt := range []struct {
		name string
		req  *pb.ListTriggersReq
	}{
		{"mine unset", pb.ListTriggersReq_builder{}.Build()},
		{"mine set", pb.ListTriggersReq_builder{Mine: &mine}.Build()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := h.Trigger.ListTriggers(strangerCtx, tt.req)
			if err != nil {
				t.Fatalf("ListTriggers with no origin: %v", err)
			}

			sawOwn := false
			for _, trig := range resp.GetTriggers() {
				if trig.GetId() == victimTriggerID {
					t.Errorf("ListTriggers with no origin leaked another user's trigger %s", victimTriggerID)
				}
				if trig.GetId() == strangerTriggerID {
					sawOwn = true
				}
				if owner := trig.GetUserId(); owner != strangerID {
					t.Errorf("ListTriggers with no origin returned trigger %s owned by %q, want only the caller's (%q); "+
						"the listing fell through unscoped",
						trig.GetId(), owner, strangerID)
				}
			}

			if !sawOwn {
				t.Errorf("the caller's own trigger %s is missing, so the assertions above proved nothing; "+
					"a no-origin listing must fall back to the caller's own triggers, not to nothing",
					strangerTriggerID)
			}
		})
	}
}

// The interesting failure is "mine returned the same thing as no mine".
func TestListTriggersMineNarrowsToTheCallerWithinTheInstance(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	callerUID, callerID := registeredCaller(t, h, pool, "mine-caller")
	otherUID, _ := registeredCaller(t, h, pool, "mine-other")
	suffix := uniqueUID("mine")

	origin := callermeta.Origin{InstanceUID: "mine-instance-" + suffix, DestinationUID: "mine-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	callerListCtx := triggerCtx(callerUID, origin)
	otherListCtx := triggerCtx(otherUID, origin)

	ownID := createTriggerVia(t, h, pool, callerListCtx,
		"mine-own-phrase-"+suffix, "own", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)
	// Same instance, different owner: the row `mine` has to drop and an unset `mine` keeps.
	foreignID := createTriggerVia(t, h, pool, otherListCtx,
		"mine-foreign-phrase-"+suffix, "foreign", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	ids := func(t *testing.T, req *pb.ListTriggersReq) map[string]string {
		t.Helper()
		resp, err := h.Trigger.ListTriggers(callerListCtx, req)
		if err != nil {
			t.Fatalf("ListTriggers: %v", err)
		}
		out := make(map[string]string, len(resp.GetTriggers()))
		for _, trig := range resp.GetTriggers() {
			out[trig.GetId()] = trig.GetUserId()
		}
		return out
	}

	t.Run("mine unset keeps the instance's other triggers", func(t *testing.T) {
		got := ids(t, pb.ListTriggersReq_builder{}.Build())
		if _, ok := got[ownID]; !ok {
			t.Errorf("the caller's own trigger %s is missing from an unnarrowed listing", ownID)
		}
		if _, ok := got[foreignID]; !ok {
			t.Errorf("another user's trigger %s on the same instance is missing from an unnarrowed listing; "+
				"without `mine` the listing is the instance's, not the caller's", foreignID)
		}
	})

	t.Run("mine drops them", func(t *testing.T) {
		mine := true
		got := ids(t, pb.ListTriggersReq_builder{Mine: &mine}.Build())

		if _, ok := got[ownID]; !ok {
			t.Errorf("the caller's own trigger %s is missing from a `mine` listing", ownID)
		}
		if _, ok := got[foreignID]; ok {
			t.Errorf("another user's trigger %s came back from a `mine` listing; the field was accepted and ignored", foreignID)
		}
		for id, owner := range got {
			if owner != callerID {
				t.Errorf("`mine` returned trigger %s owned by %q, want only the caller's (%q)", id, owner, callerID)
			}
		}
	})
}

func TestCreateTriggerInvalidatesTheInstanceCache(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-cache-create")
	suffix := uniqueUID("cachecreate")

	origin := callermeta.Origin{InstanceUID: "cache-create-instance-" + suffix, DestinationUID: "cache-create-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	// Warm the cache: this loads the instance's currently empty candidate set.
	warmPhrase := "cache-create-warm-" + suffix
	if _, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{
		Instance: triggerInstanceFor(origin),
		Phrase:   &warmPhrase,
	}.Build()); err != nil {
		t.Fatalf("warm TryTrigger: %v", err)
	}

	phrase := "cache-create-phrase-" + suffix
	reply := "cache-create-reply"
	id := createTriggerVia(t, h, pool, ctx, phrase, reply, 100, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	resp, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{
		Instance: triggerInstanceFor(origin),
		Phrase:   &phrase,
	}.Build())
	if err != nil {
		t.Fatalf("TryTrigger after create: %v", err)
	}
	if resp.GetId() != id {
		t.Errorf("TryTrigger after create did not fire the new trigger (id=%q), want %q; the cache was not invalidated on create", resp.GetId(), id)
	}
}

func TestDeleteTriggerInvalidatesTheInstanceCache(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-cache-delete")
	suffix := uniqueUID("cachedelete")

	origin := callermeta.Origin{InstanceUID: "cache-delete-instance-" + suffix, DestinationUID: "cache-delete-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	phrase := "cache-delete-phrase-" + suffix
	reply := "cache-delete-reply"
	id := createTriggerVia(t, h, pool, ctx, phrase, reply, 100, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	// Warm with the trigger present: otherwise a later "didn't fire" proves nothing.
	warmResp, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{
		Instance: triggerInstanceFor(origin),
		Phrase:   &phrase,
	}.Build())
	if err != nil {
		t.Fatalf("warm TryTrigger: %v", err)
	}
	if warmResp.GetId() != id {
		t.Fatalf("warm TryTrigger did not fire (id=%q), want %q before delete", warmResp.GetId(), id)
	}

	if _, err := h.Trigger.DeleteTrigger(ctx, pb.DeleteTriggerReq_builder{Id: &id}.Build()); err != nil {
		t.Fatalf("DeleteTrigger: %v", err)
	}

	resp, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{
		Instance: triggerInstanceFor(origin),
		Phrase:   &phrase,
	}.Build())
	if err != nil {
		t.Fatalf("TryTrigger after delete: %v", err)
	}
	if resp.GetId() != "" {
		t.Errorf("TryTrigger after delete still fired %q; the cache was not invalidated on delete", resp.GetId())
	}
}
