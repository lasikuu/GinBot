//go:build integration

// The storage-contract test for callermeta's jsonb field names.
//
//	docker compose -f docker-compose.dev.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/grpc/server/...
//
// Reuses requireDatabase, registerUser, setClearance, uniqueUID (from
// user_integration_test.go) and registeredCaller (from
// reminder_integration_test.go). None are redeclared here.
package server

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// callermeta.FieldInstanceUID and FieldDestinationUID are a STORAGE contract,
// not a wire one. instance.instance_meta is matched by jsonb equality against
// rows that already exist, indexed by uq_instance_platform_meta, so a rename
// does not fail — it silently stops matching. Every guild the bot already knows
// gets a second instance row, its triggers and reminders stop resolving, and
// nothing anywhere reports an error.
//
// callermeta_test.go pins the constants' literal values. That catches a rename
// in the constant. This catches the wider failure: whatever the code now writes
// must still find a row written by the code that came before it, including the
// rows the in-flight Ruby-bot migration is producing.
//
// The fixture row is therefore inserted with a hand-written jsonb literal and
// NOT with callermeta.Origin.InstanceMeta(). Building it from the same helper
// the production path uses would make the test agree with any rename by
// construction, which is precisely the bug it exists to catch.

// preExistingInstanceMetaJSON is the shape already in the production table.
// Spelled out, deliberately: see above.
func preExistingInstanceMetaJSON(instanceUID string) string {
	return fmt.Sprintf(`{"instance_uid": %q}`, instanceUID)
}

// insertPreExistingInstance writes an instance row the way the deployed bot
// already has, and schedules removal of it and everything the RPC hangs off it.
func insertPreExistingInstance(t *testing.T, pool *pgxpool.Pool, instanceUID string) int64 {
	t.Helper()
	ctx := context.Background()

	var instanceID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO instance (platform_enum, instance_meta)
		 VALUES ($1, $2::jsonb) RETURNING id`,
		pb.Platform_PLATFORM_DISCORD.Number(), preExistingInstanceMetaJSON(instanceUID),
	).Scan(&instanceID); err != nil {
		t.Fatalf("seed pre-existing instance row: %v", err)
	}

	t.Cleanup(func() {
		// Any instance row mentioning this uid, not just the seeded one: if the
		// bug under test reappears, the duplicate has to be cleaned up too or it
		// leaks into every later run.
		if _, err := pool.Exec(ctx,
			`DELETE FROM destination WHERE instance_id IN (
			     SELECT id FROM instance WHERE instance_meta::text LIKE '%' || $1 || '%')`,
			instanceUID); err != nil {
			t.Errorf("cleanup destinations for %s: %v", instanceUID, err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM instance WHERE instance_meta::text LIKE '%' || $1 || '%'`,
			instanceUID); err != nil {
			t.Errorf("cleanup instances for %s: %v", instanceUID, err)
		}
	})

	return instanceID
}

// countInstanceRowsMentioning counts every instance row whose metadata mentions
// the uid under ANY key.
//
// Matching on the rendered jsonb text rather than on instance_meta = $1 is the
// point: an equality probe written with today's key would not see a duplicate
// created under a renamed one, which is exactly the silent split being tested
// for. The failure mode here is a second row, not an error, so the count is the
// assertion.
func countInstanceRowsMentioning(t *testing.T, pool *pgxpool.Pool, instanceUID string) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM instance WHERE instance_meta::text LIKE '%' || $1 || '%'`,
		instanceUID,
	).Scan(&count); err != nil {
		t.Fatalf("count instance rows for %s: %v", instanceUID, err)
	}

	return count
}

// TestPreExistingInstanceRowStillResolves drives a real RPC through the origin
// interceptor against a guild the bot already knows, and asserts it resolved to
// that same row rather than creating a second one.
func TestPreExistingInstanceRowStillResolves(t *testing.T) {
	pool := requireDatabase(t)
	ctx := context.Background()

	// uniqueUID produces no LIKE metacharacters (no % and no _), so the text
	// probes above stay literal.
	suffix := uniqueUID("originstorage")
	origin := callermeta.Origin{
		InstanceUID:    "preexisting-instance-" + suffix,
		DestinationUID: "preexisting-destination-" + suffix,
	}

	preExistingID := insertPreExistingInstance(t, pool, origin.InstanceUID)

	// The interceptor's own resolver, wrapped only to record what it returned.
	// The production function does the work; nothing here reimplements it.
	//
	// Atomic because the interceptor runs on a server goroutine while the test
	// reads from its own, and this suite runs under -race.
	var resolved atomic.Int64
	resolveOrigin := func(ctx context.Context, destination *pb.ReminderDestination) (int64, error) {
		id, err := db.GetOrCreateDestinationByMeta(ctx, destination)
		if err == nil {
			resolved.Store(id)
		}
		return id, err
	}

	h := newHarness(t, withResolver(db.GetUserByPlatformUID), withOriginResolver(resolveOrigin))
	callerUID, _ := registeredCaller(t, h, pool, "originstorage")

	// Any guarded RPC will do: the origin interceptor runs on all of them, and
	// only after clearance has resolved a caller.
	callCtx := originCtx(callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID), origin)
	if _, err := h.Trigger.ListTriggers(callCtx, pb.ListTriggersReq_builder{}.Build()); err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}

	resolvedDestinationID := resolved.Load()
	if resolvedDestinationID == 0 {
		t.Fatal("the origin interceptor never resolved a destination; nothing was exercised")
	}

	// 1. Exactly one instance row still mentions this guild. Two would be the
	//    silent split: both rows valid, neither an error, half the bot's data
	//    hanging off the orphan.
	if got := countInstanceRowsMentioning(t, pool, origin.InstanceUID); got != 1 {
		t.Errorf("instance rows mentioning %s = %d, want exactly 1: the origin path created a "+
			"duplicate instead of matching the row that was already there — "+
			"callermeta.FieldInstanceUID no longer matches what is stored",
			origin.InstanceUID, got)
	}

	// 2. And the destination it resolved hangs off THAT row, not some other
	//    one. The count alone would still pass if the seeded row had been
	//    matched by accident and a second guild's row reused.
	var gotInstanceID int64
	if err := pool.QueryRow(ctx,
		`SELECT instance_id FROM destination WHERE id = $1`, resolvedDestinationID,
	).Scan(&gotInstanceID); err != nil {
		t.Fatalf("read destination %d: %v", resolvedDestinationID, err)
	}
	if gotInstanceID != preExistingID {
		t.Errorf("destination %d hangs off instance %d, want the pre-existing %d",
			resolvedDestinationID, gotInstanceID, preExistingID)
	}

	// 3. And the canonical shape callermeta builds finds the pre-existing row
	//    by the same jsonb equality uq_instance_platform_meta indexes. This is
	//    the direct statement of the contract: Origin.InstanceMeta() must equal
	//    what is in the table.
	row, err := db.GetInstanceByMeta(ctx, pb.Platform_PLATFORM_DISCORD, origin.InstanceMeta())
	if err != nil {
		t.Fatalf("Origin.InstanceMeta() does not match the stored row (%v); "+
			"callermeta.FieldInstanceUID has drifted from the jsonb key on disk", err)
	}
	if row.ID != preExistingID {
		t.Errorf("GetInstanceByMeta returned instance %d, want the pre-existing %d", row.ID, preExistingID)
	}
}

// The same call made twice must not add a row either. The origin cache makes
// the second call skip the upsert entirely, so this also covers the case where
// the cache is bypassed or reset: whichever path runs, the row count is one.
func TestRepeatedCallsDoNotDuplicateAPreExistingInstance(t *testing.T) {
	pool := requireDatabase(t)

	suffix := uniqueUID("originrepeat")
	origin := callermeta.Origin{
		InstanceUID:    "repeat-instance-" + suffix,
		DestinationUID: "repeat-destination-" + suffix,
	}

	insertPreExistingInstance(t, pool, origin.InstanceUID)

	h := newHarness(t,
		withResolver(db.GetUserByPlatformUID),
		withOriginResolver(db.GetOrCreateDestinationByMeta),
	)
	callerUID, _ := registeredCaller(t, h, pool, "originrepeat")
	callCtx := originCtx(callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID), origin)

	const calls = 3
	for i := range calls {
		if _, err := h.Trigger.ListTriggers(callCtx, pb.ListTriggersReq_builder{}.Build()); err != nil {
			t.Fatalf("ListTriggers call %d: %v", i, err)
		}
	}

	if got := countInstanceRowsMentioning(t, pool, origin.InstanceUID); got != 1 {
		t.Errorf("instance rows mentioning %s = %d after %d calls, want exactly 1",
			origin.InstanceUID, got, calls)
	}
}
