//go:build integration

package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// queryCounter is a pgx.QueryTracer that only counts, so a swapped-in pool can
// prove a helper issues a constant number of queries independent of row count.
type queryCounter struct {
	mu sync.Mutex
	n  int
}

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *queryCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// withTracedPool swaps the package pool for one that counts every query
// issued over it, pointed at the exact same database, and restores the
// original on cleanup.
func withTracedPool(t *testing.T) *queryCounter {
	t.Helper()

	cfg := dbpool.Config()
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter

	traced, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open traced pool: %v", err)
	}

	original := dbpool
	dbpool = traced
	t.Cleanup(func() {
		dbpool = original
		traced.Close()
	})

	return counter
}

func TestGetTriggerInstancesBatchReturnsOneEntryPerFoundIDAndOmitsMissing(t *testing.T) {
	f := newTriggerFixture(t, "batch-instances")
	ctx := context.Background()

	withInstance := f.create(t, "batch-with-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	noInstanceID, _, err := CreateTrigger(ctx, CreateTriggerParams{
		Phrase:      "batch-without-" + f.suffix,
		Reply:       "reply",
		UserID:      f.userID,
		Chance:      10,
		Mode:        pb.TriggerMode_TRIGGER_MODE_ANY,
		InstanceIDs: []int64{f.instanceID},
	})
	if err != nil {
		t.Fatalf("CreateTrigger (scoped): %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(ctx, `DELETE FROM trigger_instance WHERE trigger_id = $1`, noInstanceID); err != nil {
			t.Errorf("unscope trigger for cleanup: %v", err)
		}
		if _, err := db().Exec(ctx, `DELETE FROM action_record WHERE subject_id = $1`, noInstanceID); err != nil {
			t.Errorf("cleanup action_record: %v", err)
		}
		if _, err := db().Exec(ctx, `DELETE FROM trigger WHERE id = $1`, noInstanceID); err != nil {
			t.Errorf("cleanup trigger: %v", err)
		}
	})
	// noInstanceID has no scope: strip the row the create call above added.
	if _, err := db().Exec(ctx, `DELETE FROM trigger_instance WHERE trigger_id = $1`, noInstanceID); err != nil {
		t.Fatalf("remove trigger_instance row: %v", err)
	}

	missingID := "0192f000-0000-7000-8000-00000000dead"

	// Duplicate ids in the input must not duplicate, or otherwise confuse, the result.
	got, err := GetTriggerInstancesBatch(ctx, []string{withInstance, withInstance, noInstanceID, missingID})
	if err != nil {
		t.Fatalf("GetTriggerInstancesBatch: %v", err)
	}

	if instances, ok := got[withInstance]; !ok || len(instances) != 1 {
		t.Errorf("got[withInstance] = %v, want exactly one instance", got[withInstance])
	} else if instances[0].GetPlatformEnum() != pb.Platform_PLATFORM_DISCORD {
		t.Errorf("instance platform = %v, want PLATFORM_DISCORD", instances[0].GetPlatformEnum())
	}

	if _, ok := got[noInstanceID]; ok {
		t.Errorf("got[noInstanceID] is present, want the trigger absent from the map (no scoped instances)")
	}
	if _, ok := got[missingID]; ok {
		t.Errorf("got[missingID] is present, want an id with no such trigger to be absent")
	}
	if len(got) != 1 {
		t.Errorf("GetTriggerInstancesBatch returned %d entries, want exactly 1", len(got))
	}
}

func TestGetFilesByIDsReturnsOneEntryPerFoundIDAndOmitsMissing(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")

	hashA := "batch-file-a-" + suffix
	idA, _, err := GetOrCreateFileByHash(ctx, hashA, "trigger/aa/"+hashA, "image/png", 10)
	if err != nil {
		t.Fatalf("create file A: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, idA); err != nil {
			t.Errorf("cleanup file A: %v", err)
		}
	})

	hashB := "batch-file-b-" + suffix
	idB, _, err := GetOrCreateFileByHash(ctx, hashB, "trigger/bb/"+hashB, "image/gif", 20)
	if err != nil {
		t.Fatalf("create file B: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, idB); err != nil {
			t.Errorf("cleanup file B: %v", err)
		}
	})

	missingID := "0192f000-0000-7000-8000-00000000beef"

	// idA repeated must not duplicate the result or confuse the count.
	got, err := GetFilesByIDs(ctx, []string{idA, idA, idB, missingID})
	if err != nil {
		t.Fatalf("GetFilesByIDs: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("GetFilesByIDs returned %d entries, want exactly 2 (duplicates and misses excluded)", len(got))
	}
	if got[idA] == nil || got[idA].MimeType != "image/png" {
		t.Errorf("got[idA] = %+v, want the file A row", got[idA])
	}
	if got[idB] == nil || got[idB].MimeType != "image/gif" {
		t.Errorf("got[idB] = %+v, want the file B row", got[idB])
	}
	if _, ok := got[missingID]; ok {
		t.Error("a nonexistent id was present in the result")
	}
}

// TestGetFilesByIDsExcludesSoftDeletedFiles: a soft-deleted file must not be
// silently returned as if it still exists.
func TestGetFilesByIDsExcludesSoftDeletedFiles(t *testing.T) {
	ctx := context.Background()
	hash := "batch-file-deleted-" + time.Now().Format("150405.000000000")

	id, _, err := GetOrCreateFileByHash(ctx, hash, "trigger/dd/"+hash, "image/png", 10)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup file: %v", err)
		}
	})
	if err := SoftDeleteFile(ctx, id); err != nil {
		t.Fatalf("SoftDeleteFile: %v", err)
	}

	got, err := GetFilesByIDs(ctx, []string{id})
	if err != nil {
		t.Fatalf("GetFilesByIDs: %v", err)
	}
	if _, ok := got[id]; ok {
		t.Error("a soft-deleted file was returned by GetFilesByIDs")
	}
}

// TestGetTriggerInstancesBatchQueryCountIsConstant:
// counting queries at the batch-helper layer, since ListTriggers's own pool is
// not reachable from outside package db. Two very different row counts must
// cost the identical, small number of queries.
func TestGetTriggerInstancesBatchQueryCountIsConstant(t *testing.T) {
	f := newTriggerFixture(t, "batch-count")
	ctx := context.Background()

	few := []string{f.create(t, "count-few-1-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)}

	many := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, f.create(t, "count-many-"+f.suffix+"-", pb.TriggerMode_TRIGGER_MODE_ANY, 10))
	}

	counter := withTracedPool(t)
	if _, err := GetTriggerInstancesBatch(ctx, few); err != nil {
		t.Fatalf("GetTriggerInstancesBatch(few): %v", err)
	}
	afterFew := counter.count()
	if afterFew != 1 {
		t.Fatalf("GetTriggerInstancesBatch(1 id) issued %d queries, want 1", afterFew)
	}

	if _, err := GetTriggerInstancesBatch(ctx, many); err != nil {
		t.Fatalf("GetTriggerInstancesBatch(many): %v", err)
	}
	afterMany := counter.count() - afterFew
	if afterMany != 1 {
		t.Errorf("GetTriggerInstancesBatch(%d ids) issued %d additional queries, want 1 (constant, independent of row count)",
			len(many), afterMany)
	}
}

func TestGetFilesByIDsQueryCountIsConstant(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")

	few := make([]string, 0, 1)
	hash := "count-few-" + suffix
	id, _, err := GetOrCreateFileByHash(ctx, hash, "trigger/ff/"+hash, "image/png", 10)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup file: %v", err)
		}
	})
	few = append(few, id)

	many := make([]string, 0, 15)
	for i := 0; i < 15; i++ {
		h := "count-many-" + suffix + "-"
		h += time.Now().Format("000000000")
		fid, _, err := GetOrCreateFileByHash(ctx, h, "trigger/gg/"+h, "image/png", 10)
		if err != nil {
			t.Fatalf("create file %d: %v", i, err)
		}
		t.Cleanup(func() {
			if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, fid); err != nil {
				t.Errorf("cleanup file: %v", err)
			}
		})
		many = append(many, fid)
	}

	counter := withTracedPool(t)
	if _, err := GetFilesByIDs(ctx, few); err != nil {
		t.Fatalf("GetFilesByIDs(few): %v", err)
	}
	afterFew := counter.count()
	if afterFew != 1 {
		t.Fatalf("GetFilesByIDs(1 id) issued %d queries, want 1", afterFew)
	}

	if _, err := GetFilesByIDs(ctx, many); err != nil {
		t.Fatalf("GetFilesByIDs(many): %v", err)
	}
	afterMany := counter.count() - afterFew
	if afterMany != 1 {
		t.Errorf("GetFilesByIDs(%d ids) issued %d additional queries, want 1 (constant, independent of row count)",
			len(many), afterMany)
	}
}

func TestGetTriggerByRefResolvesTheSameRowAsGetTrigger(t *testing.T) {
	f := newTriggerFixture(t, "byref")
	ctx := context.Background()

	id := f.create(t, "byref-phrase-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	byID, err := GetTrigger(ctx, id)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}

	byRef, err := GetTriggerByRef(ctx, byID.Ref)
	if err != nil {
		t.Fatalf("GetTriggerByRef(%d): %v", byID.Ref, err)
	}
	if byRef.ID != id {
		t.Errorf("GetTriggerByRef resolved id %q, want %q", byRef.ID, id)
	}
}

// TestGetTriggerByRefForAnUnknownRefIsNotFound: the ref namespace is dense but
// finite, so a very large ref is not allocated to anything.
func TestGetTriggerByRefForAnUnknownRefIsNotFound(t *testing.T) {
	_, err := GetTriggerByRef(context.Background(), 9_000_000_000_000)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestSoftDeletedTriggerKeepsItsRefButIsNotResolvedByIt: a soft-deleted row is
// excluded from ref lookups exactly as it is from GetTrigger by id.
func TestSoftDeletedTriggerKeepsItsRefButIsNotResolvedByIt(t *testing.T) {
	f := newTriggerFixture(t, "byref-deleted")
	ctx := context.Background()

	id := f.create(t, "byref-deleted-phrase-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	before, err := GetTrigger(ctx, id)
	if err != nil {
		t.Fatalf("GetTrigger before delete: %v", err)
	}
	ref := before.Ref

	if err := SoftDeleteTriggerByUser(ctx, id, f.userID); err != nil {
		t.Fatalf("SoftDeleteTriggerByUser: %v", err)
	}

	if _, err := GetTriggerByRef(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTriggerByRef after delete: err = %v, want ErrNotFound", err)
	}

	// The ref column itself is untouched by the soft delete (only `deleted` flips).
	var storedRef int64
	if err := db().QueryRow(ctx, `SELECT ref FROM trigger WHERE id = $1`, id).Scan(&storedRef); err != nil {
		t.Fatalf("read stored ref: %v", err)
	}
	if storedRef != ref {
		t.Errorf("stored ref = %d after soft delete, want it unchanged at %d", storedRef, ref)
	}
}

func TestGetReminderByRefResolvesTheSameRowAsGetReminder(t *testing.T) {
	f := newReminderFixture(t, "remref")
	ctx := context.Background()

	id := f.create(t, time.Now().Add(time.Hour), "")

	byID, err := GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}

	byRef, err := GetReminderByRef(ctx, f.userID, byID.Ref)
	if err != nil {
		t.Fatalf("GetReminderByRef(%s, %d): %v", f.userID, byID.Ref, err)
	}
	if byRef.ID != id {
		t.Errorf("GetReminderByRef resolved id %q, want %q", byRef.ID, id)
	}
}

// TestReminderRefsArePerUserAndCannotBeUsedToCrossOwners: two users' first
// reminder both allocate ref 1, and a ref lookup scoped to one user must never
// resolve to the other's row.
func TestReminderRefsArePerUserAndCannotBeUsedToCrossOwners(t *testing.T) {
	alice := newReminderFixture(t, "ref-alice")
	bob := newReminderFixture(t, "ref-bob")
	ctx := context.Background()

	aliceReminderID := alice.create(t, time.Now().Add(time.Hour), "")
	bobReminderID := bob.create(t, time.Now().Add(time.Hour), "")

	aliceRow, err := GetReminder(ctx, aliceReminderID)
	if err != nil {
		t.Fatalf("GetReminder(alice): %v", err)
	}
	bobRow, err := GetReminder(ctx, bobReminderID)
	if err != nil {
		t.Fatalf("GetReminder(bob): %v", err)
	}
	if aliceRow.Ref != bobRow.Ref {
		t.Fatalf("alice's ref %d != bob's ref %d; refs are per-user, so each owner's "+
			"first reminder must carry the same number", aliceRow.Ref, bobRow.Ref)
	}

	byAlice, err := GetReminderByRef(ctx, alice.userID, aliceRow.Ref)
	if err != nil {
		t.Fatalf("GetReminderByRef(alice's user, shared ref): %v", err)
	}
	if byAlice.ID != aliceReminderID {
		t.Errorf("GetReminderByRef(alice's user, ref %d) resolved %q, want alice's own %q",
			aliceRow.Ref, byAlice.ID, aliceReminderID)
	}

	byBob, err := GetReminderByRef(ctx, bob.userID, bobRow.Ref)
	if err != nil {
		t.Fatalf("GetReminderByRef(bob's user, shared ref): %v", err)
	}
	if byBob.ID != bobReminderID {
		t.Errorf("GetReminderByRef(bob's user, ref %d) resolved %q, want bob's own %q",
			bobRow.Ref, byBob.ID, bobReminderID)
	}

	// A ref one owner holds and the other does not must miss, not fall through
	// to the holder's row.
	unheld := bobRow.Ref + 1000
	if _, err := GetReminderByRef(ctx, alice.userID, unheld); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetReminderByRef(alice, unheld ref %d) error = %v, want ErrNotFound", unheld, err)
	}
}

// TestSoftDeletedReminderKeepsItsRefButIsNotResolvedByIt mirrors the trigger case.
func TestSoftDeletedReminderKeepsItsRefButIsNotResolvedByIt(t *testing.T) {
	f := newReminderFixture(t, "remref-deleted")
	ctx := context.Background()

	id := f.create(t, time.Now().Add(time.Hour), "")

	before, err := GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder before delete: %v", err)
	}
	ref := before.Ref

	if err := SoftDeleteReminderByUser(ctx, id, f.userID); err != nil {
		t.Fatalf("SoftDeleteReminderByUser: %v", err)
	}

	if _, err := GetReminderByRef(ctx, f.userID, ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetReminderByRef after delete: err = %v, want ErrNotFound", err)
	}

	var storedRef int64
	if err := db().QueryRow(ctx, `SELECT ref FROM reminder WHERE id = $1`, id).Scan(&storedRef); err != nil {
		t.Fatalf("read stored ref: %v", err)
	}
	if storedRef != ref {
		t.Errorf("stored ref = %d after soft delete, want it unchanged at %d", storedRef, ref)
	}
}
