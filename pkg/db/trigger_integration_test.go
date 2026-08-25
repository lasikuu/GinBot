//go:build integration

// Integration tests for the trigger and file database layer. These require a
// live Postgres:
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/db/...
//
// TestMain, meta, cleanupUser and cleanupInstanceByMeta are declared in
// db_integration_test.go in this same package and are reused here, not
// redeclared.
package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// triggerFixture creates a user and an instance, registering their cleanup, so
// tests only need to worry about the trigger rows they create on top.
//
// Cleanup order matters: trigger.user_id has an FK to user_account with no
// ON DELETE CASCADE, so any trigger created against this fixture must be
// deleted before the user is. t.Cleanup runs LIFO, so registering the user's
// cleanup here (early) and each trigger's cleanup later (in create) makes the
// trigger cleanups run first automatically, without every call site having to
// reason about ordering itself.
type triggerFixture struct {
	userID     string
	instanceID int64
	suffix     string
}

func newTriggerFixture(t *testing.T, label string) *triggerFixture {
	t.Helper()
	ctx := context.Background()
	suffix := label + "-" + time.Now().Format("150405.000000000")

	userID, err := CreateUser(ctx, "trig-"+label, pb.Platform_PLATFORM_DISCORD, "uid-"+suffix, nil, "en")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cleanupUser(t, userID)

	instanceMeta := meta(t, map[string]string{"guild_id": "trig-instance-" + suffix})
	instanceID, err := CreateInstance(ctx, pb.Platform_PLATFORM_DISCORD, instanceMeta, "")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	cleanupInstanceByMeta(t, instanceMeta)

	return &triggerFixture{userID: userID, instanceID: instanceID, suffix: suffix}
}

// createOn creates a trigger scoped to the given instance ids, owned by the
// fixture's user, and schedules cleanup of both the row and anything recorded
// against it in action_record (which carries no FK to trigger and would
// otherwise silently leak).
func (f *triggerFixture) createOn(t *testing.T, phrase string, mode pb.TriggerMode, chance int32, instanceIDs []int64) string {
	t.Helper()
	ctx := context.Background()

	id, err := CreateTrigger(ctx, CreateTriggerParams{
		Phrase:      phrase,
		Reply:       "auto-reply-" + f.suffix,
		UserID:      f.userID,
		Chance:      chance,
		Mode:        mode,
		InstanceIDs: instanceIDs,
	})
	if err != nil {
		t.Fatalf("CreateTrigger(%q): %v", phrase, err)
	}

	t.Cleanup(func() {
		if _, err := db().Exec(ctx, `DELETE FROM action_record WHERE subject_id = $1`, id); err != nil {
			t.Errorf("cleanup action_record for trigger %s: %v", id, err)
		}
		if _, err := db().Exec(ctx, `DELETE FROM trigger WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup trigger %s: %v", id, err)
		}
	})

	return id
}

// create is createOn scoped to the fixture's own single instance, which is
// the common case.
func (f *triggerFixture) create(t *testing.T, phrase string, mode pb.TriggerMode, chance int32) string {
	t.Helper()
	return f.createOn(t, phrase, mode, chance, []int64{f.instanceID})
}

// TestCreateTriggerInsertsRowAndInstanceScopingInOneGo: CreateTrigger writes
// the trigger and its trigger_instance rows together; GetTrigger reads the row
// back and GetTriggerInstances returns the scoping.
func TestCreateTriggerInsertsRowAndInstanceScopingInOneGo(t *testing.T) {
	f := newTriggerFixture(t, "create")
	ctx := context.Background()

	phrase := "hello-" + f.suffix
	id := f.create(t, phrase, pb.TriggerMode_TRIGGER_MODE_ANY, 25)

	row, err := GetTrigger(ctx, id)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if row.Phrase != phrase {
		t.Errorf("phrase = %q, want %q", row.Phrase, phrase)
	}
	if row.Chance != 25 {
		t.Errorf("chance = %d, want 25", row.Chance)
	}
	if row.Mode != int32(pb.TriggerMode_TRIGGER_MODE_ANY.Number()) {
		t.Errorf("mode = %d, want %d (ANY)", row.Mode, pb.TriggerMode_TRIGGER_MODE_ANY.Number())
	}
	if row.UserID == nil || *row.UserID != f.userID {
		t.Errorf("user_id = %v, want %q", row.UserID, f.userID)
	}

	instances, err := GetTriggerInstances(ctx, id)
	if err != nil {
		t.Fatalf("GetTriggerInstances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	if instances[0].GetPlatformEnum() != pb.Platform_PLATFORM_DISCORD {
		t.Errorf("instance platform = %v, want PLATFORM_DISCORD", instances[0].GetPlatformEnum())
	}
}

// TestExactPhraseUniqueConstraintIsCaseInsensitiveAndLiveOnly is AC3: the
// database enforces global, case-insensitive uniqueness among LIVE exact-mode
// rows only.
func TestExactPhraseUniqueConstraintIsCaseInsensitiveAndLiveOnly(t *testing.T) {
	f := newTriggerFixture(t, "exact-uniq")
	ctx := context.Background()

	upper := "UNIQUE-EXACT-" + f.suffix
	lower := strings.ToLower(upper)

	f.create(t, upper, pb.TriggerMode_TRIGGER_MODE_EXACT, 10)

	_, err := CreateTrigger(ctx, CreateTriggerParams{
		Phrase:      lower,
		Reply:       "second attempt",
		UserID:      f.userID,
		Chance:      10,
		Mode:        pb.TriggerMode_TRIGGER_MODE_EXACT,
		InstanceIDs: []int64{f.instanceID},
	})
	if !errors.Is(err, ErrExactPhraseTaken) {
		t.Errorf("second exact-mode create err = %v, want ErrExactPhraseTaken", err)
	}
}

// TestTwoAnyModePhrasesMayRepeat: unlike exact mode, any-mode phrases are not
// constrained, so the same phrase may be registered more than once.
func TestTwoAnyModePhrasesMayRepeat(t *testing.T) {
	f := newTriggerFixture(t, "any-repeat")

	phrase := "repeatable-" + f.suffix
	id1 := f.create(t, phrase, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	id2 := f.create(t, phrase, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	if id1 == id2 {
		t.Fatal("two creates returned the same id")
	}
}

// TestSoftDeletedExactTriggerDoesNotBlockReuseOfItsPhrase: the unique index is
// partial (WHERE mode = 1 AND deleted = FALSE), so soft-deleting an exact
// trigger frees its phrase for reuse.
func TestSoftDeletedExactTriggerDoesNotBlockReuseOfItsPhrase(t *testing.T) {
	f := newTriggerFixture(t, "exact-reuse")
	ctx := context.Background()

	phrase := "reusable-exact-" + f.suffix
	firstID := f.create(t, phrase, pb.TriggerMode_TRIGGER_MODE_EXACT, 10)

	if err := SoftDeleteTriggerByUser(ctx, firstID, f.userID); err != nil {
		t.Fatalf("SoftDeleteTriggerByUser: %v", err)
	}

	// Must succeed now that the only other holder of the phrase is soft-deleted.
	secondID := f.create(t, phrase, pb.TriggerMode_TRIGGER_MODE_EXACT, 10)
	if secondID == firstID {
		t.Fatal("second create returned the first (soft-deleted) id")
	}
}

// TestListActiveTriggersByInstanceScopesPerInstance is AC15's db-layer half: a
// trigger scoped to instance A is returned for A and NOT for B.
func TestListActiveTriggersByInstanceScopesPerInstance(t *testing.T) {
	f := newTriggerFixture(t, "scope-a")
	ctx := context.Background()

	otherInstanceMeta := meta(t, map[string]string{"guild_id": "scope-b-" + f.suffix})
	instanceB, err := CreateInstance(ctx, pb.Platform_PLATFORM_DISCORD, otherInstanceMeta, "")
	if err != nil {
		t.Fatalf("CreateInstance (B): %v", err)
	}
	cleanupInstanceByMeta(t, otherInstanceMeta)

	phrase := "scoped-" + f.suffix
	id := f.create(t, phrase, pb.TriggerMode_TRIGGER_MODE_ANY, 10) // scoped only to f.instanceID (instance A)

	activeA, err := ListActiveTriggersByInstance(ctx, f.instanceID)
	if err != nil {
		t.Fatalf("ListActiveTriggersByInstance(A): %v", err)
	}
	if !containsActiveID(activeA, id) {
		t.Errorf("trigger %s missing from instance A's active list", id)
	}

	activeB, err := ListActiveTriggersByInstance(ctx, instanceB)
	if err != nil {
		t.Fatalf("ListActiveTriggersByInstance(B): %v", err)
	}
	if containsActiveID(activeB, id) {
		t.Errorf("trigger %s scoped only to instance A leaked into instance B's active list", id)
	}
}

func containsActiveID(actives []ActiveTrigger, id string) bool {
	for _, a := range actives {
		if a.ID == id {
			return true
		}
	}
	return false
}

// TestListActiveTriggersByInstanceExcludesSoftDeletedAndOrdersDeterministically.
func TestListActiveTriggersByInstanceExcludesSoftDeletedAndOrdersDeterministically(t *testing.T) {
	f := newTriggerFixture(t, "order")
	ctx := context.Background()

	id1 := f.create(t, "order-1-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	id2 := f.create(t, "order-2-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	id3 := f.create(t, "order-3-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	if err := SoftDeleteTriggerByUser(ctx, id2, f.userID); err != nil {
		t.Fatalf("soft delete id2: %v", err)
	}

	active, err := ListActiveTriggersByInstance(ctx, f.instanceID)
	if err != nil {
		t.Fatalf("ListActiveTriggersByInstance: %v", err)
	}

	if containsActiveID(active, id2) {
		t.Errorf("soft-deleted trigger %s was returned as active", id2)
	}
	if !containsActiveID(active, id1) || !containsActiveID(active, id3) {
		t.Errorf("live triggers missing from active list: %+v", active)
	}

	// Deterministic order: calling it twice must produce the same sequence.
	activeAgain, err := ListActiveTriggersByInstance(ctx, f.instanceID)
	if err != nil {
		t.Fatalf("ListActiveTriggersByInstance (again): %v", err)
	}
	if !sameOrder(active, activeAgain) {
		t.Errorf("ListActiveTriggersByInstance order was not stable across calls: %+v vs %+v", active, activeAgain)
	}
}

func sameOrder(a, b []ActiveTrigger) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// TestUpdateTriggerByUserRefusesAnotherUsersRow: ErrNotFound for a trigger
// that is not the caller's, success for their own.
func TestUpdateTriggerByUserRefusesAnotherUsersRow(t *testing.T) {
	owner := newTriggerFixture(t, "upd-owner")
	attacker := newTriggerFixture(t, "upd-attacker")
	ctx := context.Background()

	id := owner.create(t, "update-target-"+owner.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	newReply := "hijacked"
	err := UpdateTriggerByUser(ctx, TriggerUpdate{
		ID:          id,
		UserID:      attacker.userID,
		UpdateReply: true,
		Reply:       newReply,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("attacker's UpdateTriggerByUser err = %v, want ErrNotFound", err)
	}

	// The owner's own update must succeed.
	err = UpdateTriggerByUser(ctx, TriggerUpdate{
		ID:          id,
		UserID:      owner.userID,
		UpdateReply: true,
		Reply:       newReply,
	})
	if err != nil {
		t.Fatalf("owner's UpdateTriggerByUser: %v", err)
	}

	row, err := GetTrigger(ctx, id)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if row.Reply == nil || *row.Reply != newReply {
		t.Errorf("reply = %v, want %q", row.Reply, newReply)
	}
}

// TestSoftDeleteTriggerByUserRefusesAnotherUsersRow: same ownership boundary
// for delete.
func TestSoftDeleteTriggerByUserRefusesAnotherUsersRow(t *testing.T) {
	owner := newTriggerFixture(t, "del-owner")
	attacker := newTriggerFixture(t, "del-attacker")
	ctx := context.Background()

	id := owner.create(t, "delete-target-"+owner.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	if err := SoftDeleteTriggerByUser(ctx, id, attacker.userID); !errors.Is(err, ErrNotFound) {
		t.Errorf("attacker's SoftDeleteTriggerByUser err = %v, want ErrNotFound", err)
	}

	// Still readable: the attacker's attempt must not have deleted it.
	if _, err := GetTrigger(ctx, id); err != nil {
		t.Fatalf("GetTrigger after a foreign delete attempt: %v", err)
	}

	if err := SoftDeleteTriggerByUser(ctx, id, owner.userID); err != nil {
		t.Fatalf("owner's SoftDeleteTriggerByUser: %v", err)
	}
	if _, err := GetTrigger(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTrigger after the owner's delete err = %v, want ErrNotFound", err)
	}
}

// TestDeletingATriggerCascadesToTriggerInstance: the FK is ON DELETE CASCADE.
func TestDeletingATriggerCascadesToTriggerInstance(t *testing.T) {
	f := newTriggerFixture(t, "cascade")
	ctx := context.Background()

	id := f.create(t, "cascade-target-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	var before int
	if err := db().QueryRow(ctx, `SELECT COUNT(*) FROM trigger_instance WHERE trigger_id = $1`, id).Scan(&before); err != nil {
		t.Fatalf("count trigger_instance before delete: %v", err)
	}
	if before == 0 {
		t.Fatal("precondition failed: no trigger_instance rows to begin with")
	}

	if _, err := db().Exec(ctx, `DELETE FROM trigger WHERE id = $1`, id); err != nil {
		t.Fatalf("hard delete trigger: %v", err)
	}

	var after int
	if err := db().QueryRow(ctx, `SELECT COUNT(*) FROM trigger_instance WHERE trigger_id = $1`, id).Scan(&after); err != nil {
		t.Fatalf("count trigger_instance after delete: %v", err)
	}
	if after != 0 {
		t.Errorf("trigger_instance rows after deleting the trigger = %d, want 0 (cascade)", after)
	}
}

// TestGetOrCreateFileByHashDedupesByHash: the same hash twice yields the same
// id, inserted true then false.
func TestGetOrCreateFileByHashDedupesByHash(t *testing.T) {
	ctx := context.Background()
	hash := "hash-" + time.Now().Format("150405.000000000")

	id1, inserted1, err := GetOrCreateFileByHash(ctx, hash, "trigger/xx/"+hash, "image/png", 1024)
	if err != nil {
		t.Fatalf("first GetOrCreateFileByHash: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, id1); err != nil {
			t.Errorf("cleanup file %s: %v", id1, err)
		}
	})
	if !inserted1 {
		t.Error("first call inserted = false, want true")
	}

	id2, inserted2, err := GetOrCreateFileByHash(ctx, hash, "trigger/xx/"+hash, "image/png", 1024)
	if err != nil {
		t.Fatalf("second GetOrCreateFileByHash: %v", err)
	}
	if inserted2 {
		t.Error("second call inserted = true, want false (dedupe)")
	}
	if id2 != id1 {
		t.Errorf("second call id = %q, want %q (same as the first)", id2, id1)
	}

	var count int
	if err := db().QueryRow(ctx, `SELECT COUNT(*) FROM file WHERE file_hash = $1`, hash).Scan(&count); err != nil {
		t.Fatalf("count file rows for hash: %v", err)
	}
	if count != 1 {
		t.Errorf("file rows for hash %q = %d, want 1", hash, count)
	}
}

// TestListTriggerStatsAggregatesActionRecord is AC11: leaderboard counts,
// ordering by count descending, the period filter, instance scoping, and that
// OCCURRED and CALLED are counted separately.
func TestListTriggerStatsAggregatesActionRecord(t *testing.T) {
	fixtureA := newTriggerFixture(t, "stats-a")
	fixtureB := newTriggerFixture(t, "stats-b")
	ctx := context.Background()

	popular := fixtureA.create(t, "popular-"+fixtureA.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	quiet := fixtureA.create(t, "quiet-"+fixtureA.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	// Same phrase text is irrelevant here; what matters is a DIFFERENT instance.
	elsewhere := fixtureB.create(t, "elsewhere-"+fixtureB.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	occurred := pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED
	called := pb.ActionType_ACTION_TYPE_TRIGGER_CALLED

	for i := 0; i < 3; i++ {
		if err := RecordTriggerFire(ctx, occurred, popular, fixtureA.userID); err != nil {
			t.Fatalf("RecordTriggerFire popular occurred #%d: %v", i, err)
		}
	}
	if err := RecordTriggerFire(ctx, called, popular, fixtureA.userID); err != nil {
		t.Fatalf("RecordTriggerFire popular called: %v", err)
	}
	if err := RecordTriggerFire(ctx, occurred, quiet, fixtureA.userID); err != nil {
		t.Fatalf("RecordTriggerFire quiet occurred: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := RecordTriggerFire(ctx, occurred, elsewhere, fixtureB.userID); err != nil {
			t.Fatalf("RecordTriggerFire elsewhere occurred #%d: %v", i, err)
		}
	}

	// A row far outside any period window this test queries, so the period
	// filter has something real to exclude.
	if err := RecordTriggerFire(ctx, occurred, popular, fixtureA.userID); err != nil {
		t.Fatalf("RecordTriggerFire ancient occurred: %v", err)
	}
	if _, err := db().Exec(ctx,
		`UPDATE action_record SET action_timestamp = $1
		  WHERE subject_id = $2 AND action_type = $3
		    AND id = (SELECT MAX(id) FROM action_record WHERE subject_id = $2 AND action_type = $3)`,
		time.Now().Add(-365*24*time.Hour).UTC(), popular, int32(occurred.Number()),
	); err != nil {
		t.Fatalf("backdate one action_record row: %v", err)
	}

	// Instance scoping + ordering by count descending: popular (3, the ancient
	// one is outside the default unbounded query only if we don't filter by
	// period — without a period filter it counts all 4) ahead of quiet (1).
	statsA, err := ListTriggerStats(ctx, TriggerStatsFilter{
		InstanceID: fixtureA.instanceID,
		ActionType: occurred,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListTriggerStats(A, occurred): %v", err)
	}
	if len(statsA) != 2 {
		t.Fatalf("stats for instance A = %d rows, want 2: %+v", len(statsA), statsA)
	}
	if statsA[0].TriggerID != popular {
		t.Errorf("first row = %q, want the more popular trigger %q (ordering by count desc)", statsA[0].TriggerID, popular)
	}
	if statsA[0].Count <= statsA[1].Count {
		t.Errorf("counts not descending: %d then %d", statsA[0].Count, statsA[1].Count)
	}

	// Instance scoping: instance B's trigger must never appear in A's stats.
	for _, row := range statsA {
		if row.TriggerID == elsewhere {
			t.Errorf("instance B's trigger %s leaked into instance A's stats", elsewhere)
		}
	}

	// The period filter: excluding the backdated row drops popular's count by
	// exactly one (from 4 to 3).
	statsWithPeriod, err := ListTriggerStats(ctx, TriggerStatsFilter{
		InstanceID:  fixtureA.instanceID,
		ActionType:  occurred,
		PeriodStart: timePtr(time.Now().Add(-time.Hour)),
		PeriodEnd:   timePtr(time.Now().Add(time.Hour)),
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListTriggerStats with period: %v", err)
	}
	popularCountInPeriod := int64(0)
	for _, row := range statsWithPeriod {
		if row.TriggerID == popular {
			popularCountInPeriod = row.Count
		}
	}
	if popularCountInPeriod != 3 {
		t.Errorf("popular's in-period occurred count = %d, want 3 (the backdated 4th excluded)", popularCountInPeriod)
	}

	// OCCURRED and CALLED are counted separately: popular has 1 CALLED, not
	// merged into its OCCURRED count.
	statsCalled, err := ListTriggerStats(ctx, TriggerStatsFilter{
		InstanceID: fixtureA.instanceID,
		ActionType: called,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListTriggerStats(A, called): %v", err)
	}
	if len(statsCalled) != 1 {
		t.Fatalf("called stats for instance A = %d rows, want 1: %+v", len(statsCalled), statsCalled)
	}
	if statsCalled[0].TriggerID != popular || statsCalled[0].Count != 1 {
		t.Errorf("called stats = %+v, want popular with count 1", statsCalled[0])
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// TestListOrphanFilesExcludesFilesReferencedByALiveTrigger is AC's file GC
// query: a file with no referencing live trigger is returned; one that is
// referenced is not.
func TestListOrphanFilesExcludesFilesReferencedByALiveTrigger(t *testing.T) {
	f := newTriggerFixture(t, "orphan")
	ctx := context.Background()
	suffix := f.suffix

	orphanHash := "orphan-hash-" + suffix
	orphanID, _, err := GetOrCreateFileByHash(ctx, orphanHash, "trigger/oo/"+orphanHash, "image/png", 100)
	if err != nil {
		t.Fatalf("create orphan file: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, orphanID); err != nil {
			t.Errorf("cleanup orphan file %s: %v", orphanID, err)
		}
	})

	referencedHash := "referenced-hash-" + suffix
	referencedID, _, err := GetOrCreateFileByHash(ctx, referencedHash, "trigger/rr/"+referencedHash, "image/png", 100)
	if err != nil {
		t.Fatalf("create referenced file: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, referencedID); err != nil {
			t.Errorf("cleanup referenced file %s: %v", referencedID, err)
		}
	})

	// A trigger whose only content is the file (no reply needed: file_id alone
	// satisfies chk_reply_or_file).
	_ = f.createTriggerWithFile(t, "file-trigger-"+suffix, referencedID)

	olderThan := time.Now().Add(time.Hour).UTC() // safely after both files' created_at
	orphans, err := ListOrphanFiles(ctx, olderThan, 100)
	if err != nil {
		t.Fatalf("ListOrphanFiles: %v", err)
	}

	if !containsFileID(orphans, orphanID) {
		t.Errorf("unreferenced file %s missing from ListOrphanFiles", orphanID)
	}
	if containsFileID(orphans, referencedID) {
		t.Errorf("referenced file %s incorrectly returned by ListOrphanFiles", referencedID)
	}
}

// createTriggerWithFile creates a trigger whose response is a file rather
// than a reply, scoped to the fixture's instance.
func (f *triggerFixture) createTriggerWithFile(t *testing.T, phrase string, fileID string) string {
	t.Helper()
	ctx := context.Background()

	id, err := CreateTrigger(ctx, CreateTriggerParams{
		Phrase:      phrase,
		FileID:      fileID,
		UserID:      f.userID,
		Chance:      10,
		Mode:        pb.TriggerMode_TRIGGER_MODE_ANY,
		InstanceIDs: []int64{f.instanceID},
	})
	if err != nil {
		t.Fatalf("CreateTrigger with file: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(ctx, `DELETE FROM trigger WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup file-trigger %s: %v", id, err)
		}
	})
	return id
}

func containsFileID(files []*model.File, id string) bool {
	for _, f := range files {
		if f.ID == id {
			return true
		}
	}
	return false
}
