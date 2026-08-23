//go:build integration

// Integration tests for ListTriggers and ListTriggerInstanceIDs. These
// require a live Postgres:
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/db/...
//
// TestMain, meta, cleanupUser and cleanupInstanceByMeta are declared in
// db_integration_test.go; newTriggerFixture, triggerFixture.create and
// triggerFixture.createOn are declared in trigger_integration_test.go. All are
// in this same package and are reused here, not redeclared.
package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

// createWithReply is createOn plus an explicit reply, for tests that need to
// search on reply text rather than the fixture's fixed "auto-reply-<suffix>".
func (f *triggerFixture) createWithReply(t *testing.T, phrase, reply string, mode pb.TriggerMode, instanceIDs []int64) string {
	t.Helper()
	ctx := context.Background()

	id, err := CreateTrigger(ctx, CreateTriggerParams{
		Phrase:      phrase,
		Reply:       reply,
		UserID:      f.userID,
		Chance:      10,
		Mode:        mode,
		InstanceIDs: instanceIDs,
	})
	if err != nil {
		t.Fatalf("CreateTrigger(%q, reply=%q): %v", phrase, reply, err)
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

func containsTriggerID(triggers []*model.Trigger, id string) bool {
	for _, tr := range triggers {
		if tr.ID == id {
			return true
		}
	}
	return false
}

// TestListTriggersFiltersByUserID: only the given creator's triggers come
// back; another user's trigger, even one that would otherwise match, is
// excluded.
func TestListTriggersFiltersByUserID(t *testing.T) {
	owner := newTriggerFixture(t, "list-user-owner")
	other := newTriggerFixture(t, "list-user-other")
	ctx := context.Background()

	ownerID := owner.create(t, "owner-phrase-"+owner.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	otherID := other.create(t, "other-phrase-"+other.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	results, err := ListTriggers(ctx, ListTriggersFilter{UserID: owner.userID})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if !containsTriggerID(results, ownerID) {
		t.Errorf("owner's trigger %s missing from ListTriggers(UserID=owner)", ownerID)
	}
	if containsTriggerID(results, otherID) {
		t.Errorf("other user's trigger %s leaked into ListTriggers(UserID=owner)", otherID)
	}
}

// TestListTriggersFiltersByInstanceID: only triggers scoped to the given
// instance come back. Two instances prove the negative.
func TestListTriggersFiltersByInstanceID(t *testing.T) {
	f := newTriggerFixture(t, "list-instance")
	ctx := context.Background()

	otherInstanceMeta := meta(t, map[string]string{"guild_id": "list-instance-b-" + f.suffix})
	instanceB, err := CreateInstance(ctx, pb.Platform_PLATFORM_DISCORD, otherInstanceMeta, "")
	if err != nil {
		t.Fatalf("CreateInstance (B): %v", err)
	}
	cleanupInstanceByMeta(t, otherInstanceMeta)

	onA := f.create(t, "scoped-a-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10) // scoped to f.instanceID
	onB := f.createOn(t, "scoped-b-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10, []int64{instanceB})

	results, err := ListTriggers(ctx, ListTriggersFilter{InstanceID: f.instanceID, UserID: f.userID})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if !containsTriggerID(results, onA) {
		t.Errorf("trigger %s scoped to instance A missing from ListTriggers(InstanceID=A)", onA)
	}
	if containsTriggerID(results, onB) {
		t.Errorf("trigger %s scoped only to instance B leaked into ListTriggers(InstanceID=A)", onB)
	}
}

// TestListTriggersPhraseSearchIsPartialAndCaseInsensitive: PhraseSearch
// matches a substring regardless of case, and excludes a phrase that does not
// contain it.
func TestListTriggersPhraseSearchIsPartialAndCaseInsensitive(t *testing.T) {
	f := newTriggerFixture(t, "list-phrase")
	ctx := context.Background()

	needle := "NeedleXYZ-" + f.suffix
	matchID := f.create(t, "prefix-"+needle+"-suffix", pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	nonMatchID := f.create(t, "unrelated-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	// Search with a lowercased substring of the mixed-case phrase: this only
	// matches if the ILIKE comparison is case-insensitive.
	results, err := ListTriggers(ctx, ListTriggersFilter{
		UserID:       f.userID,
		PhraseSearch: strings.ToLower(needle),
	})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if !containsTriggerID(results, matchID) {
		t.Errorf("trigger %s containing the needle missing from PhraseSearch results", matchID)
	}
	if containsTriggerID(results, nonMatchID) {
		t.Errorf("trigger %s not containing the needle leaked into PhraseSearch results", nonMatchID)
	}
}

// TestListTriggersReplySearchIsPartialAndCaseInsensitive: same as the phrase
// case, against the reply column.
func TestListTriggersReplySearchIsPartialAndCaseInsensitive(t *testing.T) {
	f := newTriggerFixture(t, "list-reply")
	ctx := context.Background()

	needle := "ReplyNeedleXYZ-" + f.suffix
	matchID := f.createWithReply(t, "match-phrase-"+f.suffix, "prefix-"+needle+"-suffix",
		pb.TriggerMode_TRIGGER_MODE_ANY, []int64{f.instanceID})
	nonMatchID := f.createWithReply(t, "other-phrase-"+f.suffix, "unrelated-reply-"+f.suffix,
		pb.TriggerMode_TRIGGER_MODE_ANY, []int64{f.instanceID})

	results, err := ListTriggers(ctx, ListTriggersFilter{
		UserID:      f.userID,
		ReplySearch: strings.ToLower(needle),
	})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if !containsTriggerID(results, matchID) {
		t.Errorf("trigger %s whose reply contains the needle missing from ReplySearch results", matchID)
	}
	if containsTriggerID(results, nonMatchID) {
		t.Errorf("trigger %s whose reply does not contain the needle leaked into ReplySearch results", nonMatchID)
	}
}

// TestListTriggersFiltersByMode: filtering to one TriggerMode excludes the
// others.
func TestListTriggersFiltersByMode(t *testing.T) {
	f := newTriggerFixture(t, "list-mode")
	ctx := context.Background()

	anyID := f.create(t, "mode-any-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	regexID := f.create(t, "mode-regex-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_REGEX, 10)

	wantMode := int32(pb.TriggerMode_TRIGGER_MODE_ANY.Number())
	results, err := ListTriggers(ctx, ListTriggersFilter{UserID: f.userID, Mode: &wantMode})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if !containsTriggerID(results, anyID) {
		t.Errorf("ANY-mode trigger %s missing from ListTriggers(Mode=ANY)", anyID)
	}
	if containsTriggerID(results, regexID) {
		t.Errorf("REGEX-mode trigger %s leaked into ListTriggers(Mode=ANY)", regexID)
	}
}

// TestListTriggersFiltersByCreatedAtPeriod: a trigger backdated outside the
// [PeriodStart, PeriodEnd] window is excluded; one left at its natural
// creation time (now) is included.
func TestListTriggersFiltersByCreatedAtPeriod(t *testing.T) {
	f := newTriggerFixture(t, "list-period")
	ctx := context.Background()

	inWindowID := f.create(t, "period-in-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	outOfWindowID := f.create(t, "period-out-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	backdated := time.Now().Add(-365 * 24 * time.Hour).UTC()
	if _, err := db().Exec(ctx, `UPDATE trigger SET created_at = $1 WHERE id = $2`, backdated, outOfWindowID); err != nil {
		t.Fatalf("backdate trigger %s: %v", outOfWindowID, err)
	}

	periodStart := time.Now().Add(-time.Hour)
	periodEnd := time.Now().Add(time.Hour)
	results, err := ListTriggers(ctx, ListTriggersFilter{
		UserID:      f.userID,
		PeriodStart: &periodStart,
		PeriodEnd:   &periodEnd,
	})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if !containsTriggerID(results, inWindowID) {
		t.Errorf("trigger %s created inside the window missing from results", inWindowID)
	}
	if containsTriggerID(results, outOfWindowID) {
		t.Errorf("trigger %s backdated outside the window leaked into results", outOfWindowID)
	}
}

// TestListTriggersLimitDefaultsAndClamps is AC7: Limit=0 falls back to
// defaultTriggerListLimit, and a Limit above maxTriggerListLimit is clamped to
// it. Both are observed directly by creating more rows than the max limit and
// counting what comes back, via one bulk INSERT rather than
// maxTriggerListLimit+1 sequential CreateTrigger calls.
func TestListTriggersLimitDefaultsAndClamps(t *testing.T) {
	f := newTriggerFixture(t, "list-limit")
	ctx := context.Background()

	const totalRows = maxTriggerListLimit + 1 // enough to observe both bounds

	var b strings.Builder
	b.WriteString(`INSERT INTO trigger (id, phrase, reply, user_id, chance, mode) VALUES `)
	args := make([]any, 0, totalRows*6)
	for i := 0; i < totalRows; i++ {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("generate uuid #%d: %v", i, err)
		}
		if i > 0 {
			b.WriteString(",")
		}
		base := i * 6
		fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5, base+6)
		args = append(args,
			id.String(),
			fmt.Sprintf("limit-%d-%s", i, f.suffix),
			"reply-"+f.suffix,
			f.userID,
			int32(10),
			int32(pb.TriggerMode_TRIGGER_MODE_ANY.Number()),
		)
	}
	if _, err := db().Exec(ctx, b.String(), args...); err != nil {
		t.Fatalf("bulk insert %d triggers: %v", totalRows, err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM trigger WHERE user_id = $1`, f.userID); err != nil {
			t.Errorf("cleanup bulk-inserted triggers for user %s: %v", f.userID, err)
		}
	})

	defaultResults, err := ListTriggers(ctx, ListTriggersFilter{UserID: f.userID})
	if err != nil {
		t.Fatalf("ListTriggers (default limit): %v", err)
	}
	if len(defaultResults) != defaultTriggerListLimit {
		t.Errorf("Limit=0 returned %d rows, want the default limit %d", len(defaultResults), defaultTriggerListLimit)
	}

	clampedResults, err := ListTriggers(ctx, ListTriggersFilter{UserID: f.userID, Limit: maxTriggerListLimit * 10})
	if err != nil {
		t.Fatalf("ListTriggers (excessive limit): %v", err)
	}
	if len(clampedResults) != maxTriggerListLimit {
		t.Errorf("Limit=%d returned %d rows, want the clamp %d", maxTriggerListLimit*10, len(clampedResults), maxTriggerListLimit)
	}
}

// TestListTriggersNegativeOffsetIsTreatedAsZero: a negative Offset must not
// reach Postgres, which rejects a negative OFFSET outright. If the clamp in
// ListTriggers were removed, this call would return a non-nil error instead
// of the same rows Offset=0 returns.
func TestListTriggersNegativeOffsetIsTreatedAsZero(t *testing.T) {
	f := newTriggerFixture(t, "list-offset")
	ctx := context.Background()

	id1 := f.create(t, "offset-1-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)
	id2 := f.create(t, "offset-2-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	zeroOffset, err := ListTriggers(ctx, ListTriggersFilter{UserID: f.userID, Offset: 0})
	if err != nil {
		t.Fatalf("ListTriggers(Offset=0): %v", err)
	}
	negativeOffset, err := ListTriggers(ctx, ListTriggersFilter{UserID: f.userID, Offset: -5})
	if err != nil {
		t.Fatalf("ListTriggers(Offset=-5) returned an error, want the negative offset to be clamped to 0: %v", err)
	}

	if len(negativeOffset) != len(zeroOffset) {
		t.Fatalf("Offset=-5 returned %d rows, Offset=0 returned %d; want them equal", len(negativeOffset), len(zeroOffset))
	}
	for i := range zeroOffset {
		if zeroOffset[i].ID != negativeOffset[i].ID {
			t.Errorf("row %d differs: Offset=0 has %s, Offset=-5 has %s", i, zeroOffset[i].ID, negativeOffset[i].ID)
		}
	}
	if !containsTriggerID(zeroOffset, id1) || !containsTriggerID(zeroOffset, id2) {
		t.Fatalf("precondition failed: both fixture triggers must appear at Offset=0")
	}
}

// TestListTriggersCombinedFiltersUsePlaceholdersCorrectly is the
// placeholder-numbering regression case: UserID, InstanceID, PhraseSearch and
// Mode are combined, and four "near miss" rows are created that each violate
// exactly one of the four predicates. If any bound parameter were numbered
// against the wrong condition, at least one near miss would incorrectly match
// or the true match would incorrectly be excluded.
func TestListTriggersCombinedFiltersUsePlaceholdersCorrectly(t *testing.T) {
	f := newTriggerFixture(t, "combo")
	otherUser := newTriggerFixture(t, "combo-other-user")
	ctx := context.Background()

	otherInstanceMeta := meta(t, map[string]string{"guild_id": "combo-instance-b-" + f.suffix})
	instanceB, err := CreateInstance(ctx, pb.Platform_PLATFORM_DISCORD, otherInstanceMeta, "")
	if err != nil {
		t.Fatalf("CreateInstance (B): %v", err)
	}
	cleanupInstanceByMeta(t, otherInstanceMeta)

	needle := "combotarget"
	matchingPhrase := "Combo-" + strings.ToUpper(needle) + "-" + f.suffix // contains "COMBOTARGET", matches needle case-insensitively

	// The one row that satisfies every predicate.
	matchID := f.create(t, matchingPhrase, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	// Wrong user: correct instance, phrase and mode, owned by someone else.
	wrongUserID := otherUser.createOn(t, matchingPhrase, pb.TriggerMode_TRIGGER_MODE_ANY, 10, []int64{f.instanceID})

	// Wrong instance: correct user, phrase and mode, scoped elsewhere.
	wrongInstanceID := f.createOn(t, matchingPhrase, pb.TriggerMode_TRIGGER_MODE_ANY, 10, []int64{instanceB})

	// Wrong phrase: correct user, instance and mode, phrase does not contain
	// the needle.
	wrongPhraseID := f.create(t, "unrelated-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10)

	// Wrong mode: correct user, instance and phrase, different mode.
	wrongModeID := f.create(t, matchingPhrase, pb.TriggerMode_TRIGGER_MODE_REGEX, 10)

	wantMode := int32(pb.TriggerMode_TRIGGER_MODE_ANY.Number())
	results, err := ListTriggers(ctx, ListTriggersFilter{
		UserID:       f.userID,
		InstanceID:   f.instanceID,
		PhraseSearch: needle,
		Mode:         &wantMode,
	})
	if err != nil {
		t.Fatalf("ListTriggers (combined filters): %v", err)
	}

	if !containsTriggerID(results, matchID) {
		t.Errorf("the fully-matching trigger %s is missing from the combined-filter results: %+v", matchID, results)
	}
	for label, id := range map[string]string{
		"wrong user":     wrongUserID,
		"wrong instance": wrongInstanceID,
		"wrong phrase":   wrongPhraseID,
		"wrong mode":     wrongModeID,
	} {
		if containsTriggerID(results, id) {
			t.Errorf("the %s trigger %s leaked into the combined-filter results", label, id)
		}
	}
	if len(results) != 1 {
		t.Errorf("combined-filter results = %d rows, want exactly 1 (the fully-matching trigger): %+v", len(results), results)
	}
}

// TestListTriggerInstanceIDsReturnsEveryScopedInstance: ListTriggerInstanceIDs
// returns every instance a trigger is scoped to, and an empty result for one
// scoped to none.
func TestListTriggerInstanceIDsReturnsEveryScopedInstance(t *testing.T) {
	f := newTriggerFixture(t, "instance-ids")
	ctx := context.Background()

	otherInstanceMeta := meta(t, map[string]string{"guild_id": "instance-ids-b-" + f.suffix})
	instanceB, err := CreateInstance(ctx, pb.Platform_PLATFORM_DISCORD, otherInstanceMeta, "")
	if err != nil {
		t.Fatalf("CreateInstance (B): %v", err)
	}
	cleanupInstanceByMeta(t, otherInstanceMeta)

	scopedID := f.createOn(t, "scoped-both-"+f.suffix, pb.TriggerMode_TRIGGER_MODE_ANY, 10, []int64{f.instanceID, instanceB})

	ids, err := ListTriggerInstanceIDs(ctx, scopedID)
	if err != nil {
		t.Fatalf("ListTriggerInstanceIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("instance ids = %v, want 2 entries", ids)
	}
	if !containsInt64(ids, f.instanceID) || !containsInt64(ids, instanceB) {
		t.Errorf("instance ids = %v, want both %d and %d", ids, f.instanceID, instanceB)
	}

	// A trigger scoped to nothing: CreateTrigger refuses an empty
	// InstanceIDs list, so this is inserted directly, bypassing it.
	unscopedUUID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generate uuid: %v", err)
	}
	unscopedID := unscopedUUID.String()
	if _, err := db().Exec(ctx,
		`INSERT INTO trigger (id, phrase, reply, user_id, chance, mode) VALUES ($1, $2, $3, $4, $5, $6)`,
		unscopedID, "unscoped-"+f.suffix, "reply-"+f.suffix, f.userID, int32(10), int32(pb.TriggerMode_TRIGGER_MODE_ANY.Number()),
	); err != nil {
		t.Fatalf("insert unscoped trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM trigger WHERE id = $1`, unscopedID); err != nil {
			t.Errorf("cleanup unscoped trigger %s: %v", unscopedID, err)
		}
	})

	unscopedIDs, err := ListTriggerInstanceIDs(ctx, unscopedID)
	if err != nil {
		t.Fatalf("ListTriggerInstanceIDs (unscoped): %v", err)
	}
	if len(unscopedIDs) != 0 {
		t.Errorf("instance ids for an unscoped trigger = %v, want empty", unscopedIDs)
	}
}

func containsInt64(vals []int64, want int64) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
