//go:build integration

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

// instance.instance_meta is matched by jsonb equality against rows that already
// exist, so a rename of callermeta.FieldInstanceUID does not fail — it silently
// duplicates every guild the bot already knows.

// preExistingInstanceMetaJSON is spelled out by hand: building it from
// Origin.InstanceMeta() would agree with any rename by construction.
func preExistingInstanceMetaJSON(instanceUID string) string {
	return fmt.Sprintf(`{"instance_uid": %q}`, instanceUID)
}

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
		// Any row mentioning this uid: a reappearing bug's duplicate must not leak onward.
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

// countInstanceRowsMentioning matches rendered jsonb text under ANY key: an equality
// probe with today's key would not see a duplicate created under a renamed one.
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

func TestPreExistingInstanceRowStillResolves(t *testing.T) {
	pool := requireDatabase(t)
	ctx := context.Background()

	// uniqueUID produces no LIKE metacharacters, so the text probes above stay literal.
	suffix := uniqueUID("originstorage")
	origin := callermeta.Origin{
		InstanceUID:    "preexisting-instance-" + suffix,
		DestinationUID: "preexisting-destination-" + suffix,
	}

	preExistingID := insertPreExistingInstance(t, pool, origin.InstanceUID)

	// Atomic: the interceptor runs on a server goroutine and this suite runs under -race.
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

	callCtx := originCtx(callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID), origin)
	if _, err := h.Trigger.ListTriggers(callCtx, pb.ListTriggersReq_builder{}.Build()); err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}

	resolvedDestinationID := resolved.Load()
	if resolvedDestinationID == 0 {
		t.Fatal("the origin interceptor never resolved a destination; nothing was exercised")
	}

	if got := countInstanceRowsMentioning(t, pool, origin.InstanceUID); got != 1 {
		t.Errorf("instance rows mentioning %s = %d, want exactly 1: the origin path created a "+
			"duplicate instead of matching the row that was already there — "+
			"callermeta.FieldInstanceUID no longer matches what is stored",
			origin.InstanceUID, got)
	}

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

	// Origin.InstanceMeta() must find the row by the jsonb equality the unique index uses.
	row, err := db.GetInstanceByMeta(ctx, pb.Platform_PLATFORM_DISCORD, origin.InstanceMeta())
	if err != nil {
		t.Fatalf("Origin.InstanceMeta() does not match the stored row (%v); "+
			"callermeta.FieldInstanceUID has drifted from the jsonb key on disk", err)
	}
	if row.ID != preExistingID {
		t.Errorf("GetInstanceByMeta returned instance %d, want the pre-existing %d", row.ID, preExistingID)
	}
}

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
