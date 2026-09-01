//go:build integration

package db

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const fixtureReminderCap = 1000

type reminderFixture struct {
	userID        string
	destinationID int64
	destination   *pb.ReminderDestination
	// origin builds the metadata in the same jsonb shape production writes.
	origin callermeta.Origin
	suffix string
	// platformUID is the owner's identity on the fixture's owner platform.
	platformUID string
}

func newReminderFixture(t *testing.T, label string) *reminderFixture {
	t.Helper()
	return newReminderFixtureOn(t, label, pb.Platform_PLATFORM_DISCORD, pb.Platform_PLATFORM_DISCORD)
}

// newReminderFixtureOn separates the owner's platform from the destination's, so
// the claim's LEFT JOIN on platform_user is exercised in both directions.
func newReminderFixtureOn(
	t *testing.T,
	label string,
	ownerPlatform pb.Platform,
	destinationPlatform pb.Platform,
) *reminderFixture {
	t.Helper()
	ctx := context.Background()
	suffix := label + "-" + time.Now().Format("150405.000000000")
	platformUID := "uid-" + suffix

	userID, err := CreateUser(ctx, "rem-"+label, ownerPlatform, platformUID, nil, "en")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cleanupUser(t, userID)

	origin := callermeta.Origin{
		InstanceUID:    "instance-" + suffix,
		DestinationUID: "destination-" + suffix,
	}
	destination := pb.ReminderDestination_builder{
		PlatformEnum:    destinationPlatform.Enum(),
		InstanceMeta:    origin.InstanceMeta(),
		DestinationMeta: origin.DestinationMeta(),
	}.Build()
	cleanupInstanceByMeta(t, destination.GetInstanceMeta())

	destinationID, err := GetOrCreateDestinationByMeta(ctx, destination)
	if err != nil {
		t.Fatalf("GetOrCreateDestinationByMeta: %v", err)
	}

	return &reminderFixture{
		userID:        userID,
		destinationID: destinationID,
		destination:   destination,
		origin:        origin,
		suffix:        suffix,
		platformUID:   platformUID,
	}
}

func (f *reminderFixture) create(t *testing.T, fireAt time.Time, repeatCron string) string {
	t.Helper()
	ctx := context.Background()

	message := "msg-" + f.suffix
	b := pb.CreateReminderReq_builder{
		Datetime:    timestamppb.New(fireAt.UTC()),
		Timezone:    strPtr("Europe/Helsinki"),
		Message:     &message,
		Destination: f.destination,
	}
	if repeatCron != "" {
		b.RepeatCron = &repeatCron
	}
	id, err := CreateReminder(ctx, b.Build(), f.userID, f.destinationID, fixtureReminderCap)
	if err != nil {
		t.Fatalf("CreateReminder: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(ctx, `DELETE FROM reminder WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup reminder %s: %v", id, err)
		}
	})
	return id
}

func strPtr(s string) *string { return &s }

func listOwn(ctx context.Context, userID string) ([]ListedReminder, error) {
	return ListRemindersByUser(ctx, ListRemindersFilter{UserID: userID})
}

func claimedByID(rs []ClaimedReminder, id string) (ClaimedReminder, bool) {
	for _, r := range rs {
		if r.ID == id {
			return r, true
		}
	}
	return ClaimedReminder{}, false
}

func containsID(rs []ClaimedReminder, id string) bool {
	_, ok := claimedByID(rs, id)
	return ok
}

func readReminderClaim(t *testing.T, id string) (claimedAt *time.Time, attempts int32) {
	t.Helper()
	if err := db().QueryRow(context.Background(),
		`SELECT claimed_at, delivery_attempts FROM reminder WHERE id = $1`, id,
	).Scan(&claimedAt, &attempts); err != nil {
		t.Fatalf("read claim columns for %s: %v", id, err)
	}
	return claimedAt, attempts
}

func readReminderStatus(t *testing.T, id string) int32 {
	t.Helper()
	var status int32
	if err := db().QueryRow(context.Background(),
		`SELECT status FROM reminder WHERE id = $1`, id,
	).Scan(&status); err != nil {
		t.Fatalf("read status for %s: %v", id, err)
	}
	return status
}

func statusOf(s pb.ReminderStatus) int32 { return int32(s.Number()) }

func TestListRemindersScopesToOwner(t *testing.T) {
	ctx := context.Background()
	owner := newReminderFixture(t, "owner")
	other := newReminderFixture(t, "other")

	future := time.Now().Add(time.Hour)
	mine := owner.create(t, future, "")
	theirs := other.create(t, future, "")

	list, err := listOwn(ctx, owner.userID)
	if err != nil {
		t.Fatalf("ListReminders: %v", err)
	}

	seen := map[string]bool{}
	for _, r := range list {
		seen[r.Reminder.ID] = true
		if r.Reminder.UserID == nil || *r.Reminder.UserID != owner.userID {
			t.Errorf("ListReminders returned reminder %s owned by %v, not %s",
				r.Reminder.ID, r.Reminder.UserID, owner.userID)
		}
	}
	if !seen[mine] {
		t.Errorf("owner's own reminder %s missing from their list", mine)
	}
	if seen[theirs] {
		t.Errorf("owner's list leaked another user's reminder %s", theirs)
	}
}

func TestListRemindersJoinsTheDestination(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "list-join")

	id := f.create(t, time.Now().Add(time.Hour), "")

	list, err := listOwn(ctx, f.userID)
	if err != nil {
		t.Fatalf("ListReminders: %v", err)
	}

	var found *ListedReminder
	for i := range list {
		if list[i].Reminder.ID == id {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("reminder %s missing from the listing", id)
	}
	if found.Destination == nil {
		t.Fatal("listed reminder carries no destination; the join did not resolve it")
	}
	if got := found.Destination.GetPlatformEnum(); got != pb.Platform_PLATFORM_DISCORD {
		t.Errorf("destination platform = %v, want PLATFORM_DISCORD", got)
	}
	gotUID := found.Destination.GetDestinationMeta().GetFields()[callermeta.FieldDestinationUID].GetStringValue()
	if gotUID != f.origin.DestinationUID {
		t.Errorf("destination uid = %q, want %q", gotUID, f.origin.DestinationUID)
	}
}

func TestListRemindersOrdersSoonestFirst(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "order")

	base := time.Now().Add(time.Hour)
	far := f.create(t, base.Add(72*time.Hour), "")
	near := f.create(t, base.Add(time.Minute), "")
	middle := f.create(t, base.Add(24*time.Hour), "")

	list, err := listOwn(ctx, f.userID)
	if err != nil {
		t.Fatalf("ListReminders: %v", err)
	}

	var order []string
	for _, r := range list {
		order = append(order, r.Reminder.ID)
	}
	want := []string{near, middle, far}
	if len(order) != len(want) {
		t.Fatalf("listing returned %d reminders, want %d (%v)", len(order), len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("listing order = %v, want soonest first %v", order, want)
		}
	}

	for i := 1; i < len(list); i++ {
		if list[i].Reminder.Datetime.Before(list[i-1].Reminder.Datetime) {
			t.Errorf("listing is not ascending: %v precedes %v",
				list[i-1].Reminder.Datetime, list[i].Reminder.Datetime)
		}
	}
}

func TestUpdateReminderRefusesOtherUsersRow(t *testing.T) {
	ctx := context.Background()
	owner := newReminderFixture(t, "upd-owner")
	attacker := newReminderFixture(t, "upd-attacker")

	id := owner.create(t, time.Now().Add(time.Hour), "")

	const newMsg = "hijacked"
	err := UpdateReminderByUser(ctx, ReminderUpdate{
		ID:            id,
		UserID:        attacker.userID,
		Datetime:      time.Now().Add(time.Hour),
		Timezone:      "Europe/Helsinki",
		UpdateMessage: true,
		Message:       newMsg,
		DestinationID: owner.destinationID,
	})
	if err != ErrNotFound {
		t.Errorf("UpdateReminder by non-owner err = %v, want ErrNotFound", err)
	}

	got, err := GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if got.Message != nil && *got.Message == newMsg {
		t.Error("non-owner update mutated the row")
	}
}

func TestUpdateReminderLeavesAnUnsuppliedRepeatAlone(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "keep-repeat")

	const schedule = "0 9 * * *"
	id := f.create(t, time.Now().Add(time.Hour), schedule)

	if err := UpdateReminderByUser(ctx, ReminderUpdate{
		ID:            id,
		UserID:        f.userID,
		Datetime:      time.Now().Add(2 * time.Hour),
		Timezone:      "Europe/Helsinki",
		UpdateMessage: true,
		Message:       "only the message changed",
		DestinationID: f.destinationID,
		UpdateRepeat:  false,
	}); err != nil {
		t.Fatalf("UpdateReminderByUser: %v", err)
	}

	got, err := GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if got.RepeatCron == nil {
		t.Fatal("a message-only edit destroyed the repeat schedule")
	}
	if *got.RepeatCron != schedule {
		t.Errorf("repeat_cron = %q, want %q untouched", *got.RepeatCron, schedule)
	}
	if got.Message == nil || *got.Message != "only the message changed" {
		t.Errorf("message = %v, want the new message", got.Message)
	}
}

func TestUpdateReminderLeavesAnUnsuppliedMessageAlone(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "keep-message")

	id := f.create(t, time.Now().Add(time.Hour), "")
	seeded := "msg-" + f.suffix

	// A datetime-only edit must not NULL the stored text.
	if err := UpdateReminderByUser(ctx, ReminderUpdate{
		ID:            id,
		UserID:        f.userID,
		Datetime:      time.Now().Add(2 * time.Hour),
		Timezone:      "Europe/Helsinki",
		DestinationID: f.destinationID,
		UpdateMessage: false,
	}); err != nil {
		t.Fatalf("UpdateReminderByUser: %v", err)
	}

	got, err := GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if got.Message == nil {
		t.Fatal("a datetime-only edit destroyed the message")
	}
	if *got.Message != seeded {
		t.Errorf("message = %q, want %q untouched", *got.Message, seeded)
	}
}

func TestUpdateReminderWritesAndClearsTheMessage(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "set-message")

	id := f.create(t, time.Now().Add(time.Hour), "")

	if err := UpdateReminderByUser(ctx, ReminderUpdate{
		ID:            id,
		UserID:        f.userID,
		Datetime:      time.Now().Add(2 * time.Hour),
		Timezone:      "Europe/Helsinki",
		DestinationID: f.destinationID,
		UpdateMessage: true,
		Message:       "replaced",
	}); err != nil {
		t.Fatalf("UpdateReminderByUser (replace): %v", err)
	}
	got, err := GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if got.Message == nil || *got.Message != "replaced" {
		t.Fatalf("message = %v, want the replacement", got.Message)
	}

	// Empty string with UpdateMessage is the clear sentinel.
	if err := UpdateReminderByUser(ctx, ReminderUpdate{
		ID:            id,
		UserID:        f.userID,
		Datetime:      time.Now().Add(3 * time.Hour),
		Timezone:      "Europe/Helsinki",
		DestinationID: f.destinationID,
		UpdateMessage: true,
		Message:       "",
	}); err != nil {
		t.Fatalf("UpdateReminderByUser (clear): %v", err)
	}
	got, err = GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if got.Message != nil {
		t.Errorf("message = %q, want NULL after the clear sentinel", *got.Message)
	}
}

func TestUpdateReminderWritesAndClearsTheRepeat(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "set-repeat")

	id := f.create(t, time.Now().Add(time.Hour), "0 9 * * *")

	if err := UpdateReminderByUser(ctx, ReminderUpdate{
		ID:            id,
		UserID:        f.userID,
		Datetime:      time.Now().Add(2 * time.Hour),
		Timezone:      "Europe/Helsinki",
		UpdateMessage: true,
		Message:       "m",
		DestinationID: f.destinationID,
		UpdateRepeat:  true,
		RepeatCron:    "@daily",
	}); err != nil {
		t.Fatalf("UpdateReminderByUser (replace): %v", err)
	}
	got, err := GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if got.RepeatCron == nil || *got.RepeatCron != "@daily" {
		t.Fatalf("repeat_cron = %v, want @daily", got.RepeatCron)
	}

	if err := UpdateReminderByUser(ctx, ReminderUpdate{
		ID:            id,
		UserID:        f.userID,
		Datetime:      time.Now().Add(3 * time.Hour),
		Timezone:      "Europe/Helsinki",
		UpdateMessage: true,
		Message:       "m",
		DestinationID: f.destinationID,
		UpdateRepeat:  true,
		RepeatCron:    "",
	}); err != nil {
		t.Fatalf("UpdateReminderByUser (clear): %v", err)
	}
	got, err = GetReminder(ctx, id)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if got.RepeatCron != nil {
		t.Errorf("repeat_cron = %q, want NULL after the clear sentinel", *got.RepeatCron)
	}
}

func TestUpdateReminderReArmsATerminalReminder(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "rearm")

	for _, terminal := range []pb.ReminderStatus{
		pb.ReminderStatus_REMINDER_STATUS_DELIVERED,
		pb.ReminderStatus_REMINDER_STATUS_FAILED,
	} {
		t.Run(terminal.String(), func(t *testing.T) {
			id := f.create(t, time.Now().Add(time.Hour), "")

			if err := SetReminderStatus(ctx, id, terminal); err != nil {
				t.Fatalf("SetReminderStatus: %v", err)
			}
			// Dirty the delivery bookkeeping too, so the reset is observable.
			if _, err := db().Exec(ctx,
				`UPDATE reminder SET claimed_at = NOW(), delivery_attempts = 4 WHERE id = $1`, id,
			); err != nil {
				t.Fatalf("seed claim bookkeeping: %v", err)
			}

			if err := UpdateReminderByUser(ctx, ReminderUpdate{
				ID:            id,
				UserID:        f.userID,
				Datetime:      time.Now().Add(2 * time.Hour),
				Timezone:      "Europe/Helsinki",
				UpdateMessage: true,
				Message:       "moved",
				DestinationID: f.destinationID,
			}); err != nil {
				t.Fatalf("UpdateReminderByUser: %v", err)
			}

			if got := readReminderStatus(t, id); got != statusOf(pb.ReminderStatus_REMINDER_STATUS_PENDING) {
				t.Errorf("status after edit = %d, want PENDING; the reminder would never fire", got)
			}
			claimedAt, attempts := readReminderClaim(t, id)
			if claimedAt != nil {
				t.Errorf("claimed_at = %v, want NULL after an edit", claimedAt)
			}
			if attempts != 0 {
				t.Errorf("delivery_attempts = %d, want 0 after an edit", attempts)
			}

			if _, err := db().Exec(ctx,
				`UPDATE reminder SET datetime = $1 WHERE id = $2`, time.Now().Add(-time.Minute).UTC(), id,
			); err != nil {
				t.Fatalf("make due: %v", err)
			}
			claimed, err := ClaimDueReminders(ctx, time.Now())
			if err != nil {
				t.Fatalf("ClaimDueReminders: %v", err)
			}
			if !containsID(claimed, id) {
				t.Error("re-armed reminder was not claimed; the edit did not restore PENDING")
			}
		})
	}
}

func TestDeleteReminderRefusesOtherUsersRow(t *testing.T) {
	ctx := context.Background()
	owner := newReminderFixture(t, "del-owner")
	attacker := newReminderFixture(t, "del-attacker")

	id := owner.create(t, time.Now().Add(time.Hour), "")

	if err := SoftDeleteReminderByUser(ctx, id, attacker.userID); err != ErrNotFound {
		t.Errorf("DeleteReminder by non-owner err = %v, want ErrNotFound", err)
	}
	if _, err := GetReminder(ctx, id); err != nil {
		t.Errorf("owner's reminder was affected by a non-owner delete: %v", err)
	}
}

func TestDeleteReminderSoftDeletes(t *testing.T) {
	ctx := context.Background()
	owner := newReminderFixture(t, "soft")
	id := owner.create(t, time.Now().Add(time.Hour), "")

	if err := SoftDeleteReminderByUser(ctx, id, owner.userID); err != nil {
		t.Fatalf("DeleteReminder: %v", err)
	}

	if _, err := GetReminder(ctx, id); err != ErrNotFound {
		t.Errorf("GetReminder after soft delete err = %v, want ErrNotFound", err)
	}

	list, err := listOwn(ctx, owner.userID)
	if err != nil {
		t.Fatalf("ListReminders: %v", err)
	}
	for _, r := range list {
		if r.Reminder.ID == id {
			t.Errorf("soft-deleted reminder %s still appears in list", id)
		}
	}

	var deleted bool
	if err := db().QueryRow(ctx, `SELECT deleted FROM reminder WHERE id = $1`, id).Scan(&deleted); err != nil {
		t.Fatalf("row was physically removed, expected a soft delete: %v", err)
	}
	if !deleted {
		t.Error("deleted flag was not set")
	}
}

func TestClaimDueRemindersIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "claim")

	const dueCount = 20
	due := make(map[string]bool, dueCount)
	past := time.Now().Add(-time.Minute)
	for i := 0; i < dueCount; i++ {
		id := f.create(t, past.Add(-time.Duration(i)*time.Second), "")
		due[id] = true
	}

	now := time.Now()

	const claimers = 8
	var (
		mu      sync.Mutex
		claimed = map[string]int{}
		start   sync.WaitGroup
		done    sync.WaitGroup
	)
	start.Add(1)
	for i := 0; i < claimers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait() // release together to maximise contention
			rows, err := ClaimDueReminders(ctx, now)
			if err != nil {
				t.Errorf("ClaimDueReminders: %v", err)
				return
			}
			mu.Lock()
			for _, r := range rows {
				claimed[r.ID]++
			}
			mu.Unlock()
		}()
	}
	start.Done()
	done.Wait()

	for id := range due {
		if claimed[id] == 0 {
			if status := readReminderStatus(t, id); status != statusOf(pb.ReminderStatus_REMINDER_STATUS_PENDING) {
				t.Errorf("reminder %s went to status %d without any claimer reporting it; "+
					"the claim moved the row without returning it, so the delivery loop would never push it",
					id, status)
			}
		}
	}

	for id := range due {
		switch claimed[id] {
		case 0:
			t.Errorf("due reminder %s was never claimed", id)
		case 1:
		default:
			t.Errorf("reminder %s was claimed %d times; the claim is not atomic", id, claimed[id])
		}
	}

	for id := range due {
		if got := readReminderStatus(t, id); got != statusOf(pb.ReminderStatus_REMINDER_STATUS_SENT) {
			t.Errorf("reminder %s status = %d, want SENT", id, got)
		}
		claimedAt, attempts := readReminderClaim(t, id)
		if claimedAt == nil {
			t.Errorf("reminder %s has no claimed_at; the reclaim would never see it", id)
		}
		if attempts != 1 {
			t.Errorf("reminder %s delivery_attempts = %d, want 1 after a single claim", id, attempts)
		}
	}
}

func TestClaimDueRemindersTwiceClaimsEachOnce(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "claim2")

	id := f.create(t, time.Now().Add(-time.Minute), "")
	now := time.Now()

	first, err := ClaimDueReminders(ctx, now)
	if err != nil {
		t.Fatalf("first ClaimDueReminders: %v", err)
	}
	second, err := ClaimDueReminders(ctx, now)
	if err != nil {
		t.Fatalf("second ClaimDueReminders: %v", err)
	}

	if !containsID(first, id) {
		t.Errorf("first claim did not include %s", id)
	}
	if containsID(second, id) {
		t.Errorf("second claim re-claimed already-SENT reminder %s", id)
	}
}

func TestClaimDueRemindersResolvesTheDeliveryPayload(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "payload")

	id := f.create(t, time.Now().Add(-time.Minute), "")

	claimed, err := ClaimDueReminders(ctx, time.Now())
	if err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}
	got, ok := claimedByID(claimed, id)
	if !ok {
		t.Fatalf("due reminder %s was not claimed", id)
	}

	if got.PlatformEnum != int32(pb.Platform_PLATFORM_DISCORD.Number()) {
		t.Errorf("PlatformEnum = %d, want %d (PLATFORM_DISCORD)",
			got.PlatformEnum, pb.Platform_PLATFORM_DISCORD.Number())
	}
	if got.DestinationUID == nil {
		t.Fatalf("DestinationUID is NULL; destination_meta->>%q did not resolve",
			callermeta.FieldDestinationUID)
	}
	if *got.DestinationUID != f.origin.DestinationUID {
		t.Errorf("DestinationUID = %q, want %q", *got.DestinationUID, f.origin.DestinationUID)
	}
	if got.OwnerPlatformUID == nil {
		t.Fatal("OwnerPlatformUID is NULL although the owner has a platform_user row for this platform")
	}
	if *got.OwnerPlatformUID != f.platformUID {
		t.Errorf("OwnerPlatformUID = %q, want %q", *got.OwnerPlatformUID, f.platformUID)
	}
	if got.Message == nil || *got.Message != "msg-"+f.suffix {
		t.Errorf("Message = %v, want %q", got.Message, "msg-"+f.suffix)
	}
}

func TestClaimDueRemindersLeavesOwnerUIDNullOnAnUnlinkedPlatform(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixtureOn(t, "unlinked",
		pb.Platform_PLATFORM_DISCORD, pb.Platform_PLATFORM_MATRIX_PROTOCOL)

	id := f.create(t, time.Now().Add(-time.Minute), "")

	claimed, err := ClaimDueReminders(ctx, time.Now())
	if err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}
	got, ok := claimedByID(claimed, id)
	if !ok {
		t.Fatal("a reminder whose owner has no platform_user row for the destination's platform was not claimed")
	}

	if got.OwnerPlatformUID != nil {
		t.Errorf("OwnerPlatformUID = %q, want NULL: the owner has no identity on this platform",
			*got.OwnerPlatformUID)
	}
	if got.DestinationUID == nil || *got.DestinationUID != f.origin.DestinationUID {
		t.Errorf("DestinationUID = %v, want %q", got.DestinationUID, f.origin.DestinationUID)
	}
	if got.PlatformEnum != int32(pb.Platform_PLATFORM_MATRIX_PROTOCOL.Number()) {
		t.Errorf("PlatformEnum = %d, want %d (PLATFORM_MATRIX_PROTOCOL)",
			got.PlatformEnum, pb.Platform_PLATFORM_MATRIX_PROTOCOL.Number())
	}
}

func TestReclaimStaleRemindersUsesClaimedAtNotUpdatedAt(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "reclaim")

	id := f.create(t, time.Now().Add(-time.Hour), "")

	// Age only the claim stamp: the UPDATE fires trg_reminder_updated_at, so
	// updated_at stays deliberately fresh.
	if _, err := ClaimDueReminders(ctx, time.Now()); err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}
	stale := time.Now().Add(-time.Hour).UTC()
	if _, err := db().Exec(ctx, `UPDATE reminder SET claimed_at = $1 WHERE id = $2`, stale, id); err != nil {
		t.Fatalf("backdate claimed_at: %v", err)
	}

	// Premise: updated_at is not stale, so a reclaim keyed on it finds nothing.
	var updatedAt time.Time
	if err := db().QueryRow(ctx, `SELECT updated_at FROM reminder WHERE id = $1`, id).Scan(&updatedAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if updatedAt.Before(stale.Add(time.Minute)) {
		t.Fatalf("test premise drifted: updated_at %v is already stale, so this proves nothing", updatedAt)
	}

	requireOnlyStaleClaims(t, id)

	outcome, err := ReclaimStaleReminders(ctx, time.Now())
	if err != nil {
		t.Fatalf("ReclaimStaleReminders: %v", err)
	}
	// Exact, not a lower bound: a larger count means the reclaim swept a row it
	// was not asked to.
	if outcome.Retried != 1 {
		t.Errorf("retried %d reminders, want exactly 1", outcome.Retried)
	}

	if got := readReminderStatus(t, id); got != statusOf(pb.ReminderStatus_REMINDER_STATUS_PENDING) {
		t.Errorf("stuck reminder status = %d, want PENDING after reclaim", got)
	}
	claimedAt, _ := readReminderClaim(t, id)
	if claimedAt != nil {
		t.Errorf("claimed_at = %v, want NULL after a reclaim", claimedAt)
	}
}

func TestReclaimStaleRemindersIgnoresAFreshClaim(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "fresh-claim")

	id := f.create(t, time.Now().Add(-time.Hour), "")
	if _, err := ClaimDueReminders(ctx, time.Now()); err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}

	// trg_reminder_updated_at rewrites updated_at on every UPDATE, so it is
	// disabled for this one statement and re-enabled immediately.
	if _, err := db().Exec(ctx, `ALTER TABLE reminder DISABLE TRIGGER trg_reminder_updated_at`); err != nil {
		t.Fatalf("disable updated_at trigger: %v", err)
	}
	_, updErr := db().Exec(ctx,
		`UPDATE reminder SET updated_at = $1 WHERE id = $2`, time.Now().Add(-time.Hour).UTC(), id)
	if _, err := db().Exec(ctx, `ALTER TABLE reminder ENABLE TRIGGER trg_reminder_updated_at`); err != nil {
		t.Fatalf("re-enable updated_at trigger: %v", err)
	}
	if updErr != nil {
		t.Fatalf("backdate updated_at: %v", updErr)
	}

	if _, err := ReclaimStaleReminders(ctx, time.Now()); err != nil {
		t.Fatalf("ReclaimStaleReminders: %v", err)
	}

	if got := readReminderStatus(t, id); got != statusOf(pb.ReminderStatus_REMINDER_STATUS_SENT) {
		t.Errorf("in-flight reminder status = %d, want SENT; a fresh claim was reclaimed and will double-post", got)
	}
}

func TestReclaimStaleRemindersGivesUpAtTheAttemptCap(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "cap")

	tests := []struct {
		name       string
		attempts   int32
		wantStatus pb.ReminderStatus
	}{
		{
			name:       "one attempt short of the cap retries",
			attempts:   maxDeliveryAttempts - 1,
			wantStatus: pb.ReminderStatus_REMINDER_STATUS_PENDING,
		},
		{
			name:       "at the cap gives up",
			attempts:   maxDeliveryAttempts,
			wantStatus: pb.ReminderStatus_REMINDER_STATUS_FAILED,
		},
		{
			name:       "past the cap gives up",
			attempts:   maxDeliveryAttempts + 3,
			wantStatus: pb.ReminderStatus_REMINDER_STATUS_FAILED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := f.create(t, time.Now().Add(-time.Hour), "")

			if _, err := db().Exec(ctx,
				`UPDATE reminder SET status = $1, claimed_at = $2, delivery_attempts = $3 WHERE id = $4`,
				statusOf(pb.ReminderStatus_REMINDER_STATUS_SENT),
				time.Now().Add(-time.Hour).UTC(), tt.attempts, id,
			); err != nil {
				t.Fatalf("seed stale claim: %v", err)
			}

			if _, err := ReclaimStaleReminders(ctx, time.Now()); err != nil {
				t.Fatalf("ReclaimStaleReminders: %v", err)
			}

			if got := readReminderStatus(t, id); got != statusOf(tt.wantStatus) {
				t.Errorf("status = %d, want %d (%v)", got, statusOf(tt.wantStatus), tt.wantStatus)
			}
			claimedAt, _ := readReminderClaim(t, id)
			if claimedAt != nil {
				t.Errorf("claimed_at = %v, want NULL", claimedAt)
			}
		})
	}
}

func TestReclaimStaleRemindersCountsBothOutcomes(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "counts")

	retryID := f.create(t, time.Now().Add(-time.Hour), "")
	giveUpID := f.create(t, time.Now().Add(-time.Hour), "")

	stale := time.Now().Add(-time.Hour).UTC()
	seed := func(id string, attempts int32) {
		if _, err := db().Exec(ctx,
			`UPDATE reminder SET status = $1, claimed_at = $2, delivery_attempts = $3 WHERE id = $4`,
			statusOf(pb.ReminderStatus_REMINDER_STATUS_SENT), stale, attempts, id,
		); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed(retryID, 1)
	seed(giveUpID, maxDeliveryAttempts)
	requireOnlyStaleClaims(t, retryID, giveUpID)

	outcome, err := ReclaimStaleReminders(ctx, time.Now())
	if err != nil {
		t.Fatalf("ReclaimStaleReminders: %v", err)
	}

	// Exact, not lower bounds: a row double-counted into both buckets would still
	// satisfy ">= 1" on each.
	if outcome.Retried != 1 {
		t.Errorf("Retried = %d, want exactly 1", outcome.Retried)
	}
	if outcome.FailedOut != 1 {
		t.Errorf("FailedOut = %d, want exactly 1", outcome.FailedOut)
	}

	if got := readReminderStatus(t, retryID); got != statusOf(pb.ReminderStatus_REMINDER_STATUS_PENDING) {
		t.Errorf("retryable reminder status = %d, want PENDING", got)
	}
	if got := readReminderStatus(t, giveUpID); got != statusOf(pb.ReminderStatus_REMINDER_STATUS_FAILED) {
		t.Errorf("capped reminder status = %d, want FAILED", got)
	}
}

func TestAdvanceReminderStatusIfSentOnlyMovesASentRow(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "advance")

	tests := []struct {
		name         string
		from         pb.ReminderStatus
		wantAdvanced bool
	}{
		{name: "from SENT", from: pb.ReminderStatus_REMINDER_STATUS_SENT, wantAdvanced: true},
		{name: "from PENDING", from: pb.ReminderStatus_REMINDER_STATUS_PENDING, wantAdvanced: false},
		{name: "from DELIVERED", from: pb.ReminderStatus_REMINDER_STATUS_DELIVERED, wantAdvanced: false},
		{name: "from FAILED", from: pb.ReminderStatus_REMINDER_STATUS_FAILED, wantAdvanced: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := f.create(t, time.Now().Add(time.Hour), "")
			if err := SetReminderStatus(ctx, id, tt.from); err != nil {
				t.Fatalf("SetReminderStatus: %v", err)
			}

			advanced, err := AdvanceReminderStatusIfSent(ctx, id, pb.ReminderStatus_REMINDER_STATUS_DELIVERED)
			if err != nil {
				t.Fatalf("AdvanceReminderStatusIfSent: %v", err)
			}
			if advanced != tt.wantAdvanced {
				t.Errorf("advanced = %v, want %v", advanced, tt.wantAdvanced)
			}

			want := tt.from
			if tt.wantAdvanced {
				want = pb.ReminderStatus_REMINDER_STATUS_DELIVERED
			}
			if got := readReminderStatus(t, id); got != statusOf(want) {
				t.Errorf("status = %d, want %d (%v)", got, statusOf(want), want)
			}
		})
	}
}

func TestAdvanceReminderStatusIfSentIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "advance-twice")

	id := f.create(t, time.Now().Add(-time.Minute), "")
	if _, err := ClaimDueReminders(ctx, time.Now()); err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}

	first, err := AdvanceReminderStatusIfSent(ctx, id, pb.ReminderStatus_REMINDER_STATUS_DELIVERED)
	if err != nil {
		t.Fatalf("first advance: %v", err)
	}
	second, err := AdvanceReminderStatusIfSent(ctx, id, pb.ReminderStatus_REMINDER_STATUS_FAILED)
	if err != nil {
		t.Fatalf("second advance: %v", err)
	}

	if !first {
		t.Error("first advance reported no change")
	}
	if second {
		t.Error("second advance reported a change; a duplicate confirm is not a no-op")
	}
	if got := readReminderStatus(t, id); got != statusOf(pb.ReminderStatus_REMINDER_STATUS_DELIVERED) {
		t.Errorf("status = %d, want DELIVERED; the duplicate confirm overwrote it", got)
	}
	claimedAt, _ := readReminderClaim(t, id)
	if claimedAt != nil {
		t.Errorf("claimed_at = %v, want NULL once the reminder left SENT", claimedAt)
	}
}

func TestRescheduleReminderIfSentOnlyMovesASentRow(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "resched")

	next := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)

	t.Run("from SENT", func(t *testing.T) {
		id := f.create(t, time.Now().Add(-time.Minute), "0 9 * * *")
		if _, err := ClaimDueReminders(ctx, time.Now()); err != nil {
			t.Fatalf("ClaimDueReminders: %v", err)
		}

		rescheduled, err := RescheduleReminderIfSent(ctx, id, next)
		if err != nil {
			t.Fatalf("RescheduleReminderIfSent: %v", err)
		}
		if !rescheduled {
			t.Fatal("rescheduled = false for a SENT reminder")
		}

		got, err := GetReminder(ctx, id)
		if err != nil {
			t.Fatalf("GetReminder: %v", err)
		}
		if got.Status != statusOf(pb.ReminderStatus_REMINDER_STATUS_PENDING) {
			t.Errorf("status = %d, want PENDING", got.Status)
		}
		if !got.Datetime.UTC().Truncate(time.Second).Equal(next) {
			t.Errorf("datetime = %v, want %v", got.Datetime.UTC(), next)
		}
		if got.ClaimedAt != nil {
			t.Errorf("claimed_at = %v, want NULL after a reschedule", got.ClaimedAt)
		}
		if got.DeliveryAttempts != 0 {
			t.Errorf("delivery_attempts = %d, want 0: this occurrence delivered", got.DeliveryAttempts)
		}
	})

	t.Run("from PENDING is a no-op", func(t *testing.T) {
		id := f.create(t, time.Now().Add(time.Hour), "0 9 * * *")
		before, err := GetReminder(ctx, id)
		if err != nil {
			t.Fatalf("GetReminder: %v", err)
		}

		rescheduled, err := RescheduleReminderIfSent(ctx, id, next)
		if err != nil {
			t.Fatalf("RescheduleReminderIfSent: %v", err)
		}
		if rescheduled {
			t.Error("rescheduled a reminder that was not SENT")
		}

		after, err := GetReminder(ctx, id)
		if err != nil {
			t.Fatalf("GetReminder: %v", err)
		}
		if !after.Datetime.Equal(before.Datetime) {
			t.Errorf("datetime moved from %v to %v on a guarded no-op", before.Datetime, after.Datetime)
		}
	})
}

func TestCountActiveRemindersCountsOnlyPendingOwned(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "count")
	other := newReminderFixture(t, "count-other")

	future := time.Now().Add(time.Hour)

	f.create(t, future, "")
	pendingID := f.create(t, future, "")

	deliveredID := f.create(t, future, "")
	if err := SetReminderStatus(ctx, deliveredID, pb.ReminderStatus_REMINDER_STATUS_DELIVERED); err != nil {
		t.Fatalf("SetReminderStatus: %v", err)
	}

	deletedID := f.create(t, future, "")
	if err := SoftDeleteReminderByUser(ctx, deletedID, f.userID); err != nil {
		t.Fatalf("SoftDeleteReminderByUser: %v", err)
	}

	other.create(t, future, "")

	count, err := CountActiveReminders(ctx, f.userID)
	if err != nil {
		t.Fatalf("CountActiveReminders: %v", err)
	}
	if count != 2 {
		t.Errorf("active count = %d, want 2 (only owned pending: %s and one more)", count, pendingID)
	}
}

func TestCreateReminderRefusesAtTheCap(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "cap-refuse")

	const cap = 2
	future := time.Now().Add(time.Hour)
	message := "capped"
	req := pb.CreateReminderReq_builder{
		Datetime:    timestamppb.New(future.UTC()),
		Timezone:    strPtr("Europe/Helsinki"),
		Message:     &message,
		Destination: f.destination,
	}.Build()

	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(),
			`DELETE FROM reminder WHERE user_id = $1`, f.userID); err != nil {
			t.Errorf("cleanup reminders for %s: %v", f.userID, err)
		}
	})

	for i := 0; i < cap; i++ {
		if _, err := CreateReminder(ctx, req, f.userID, f.destinationID, cap); err != nil {
			t.Fatalf("CreateReminder %d of %d: %v", i+1, cap, err)
		}
	}

	if _, err := CreateReminder(ctx, req, f.userID, f.destinationID, cap); err != ErrReminderCapReached {
		t.Errorf("create past the cap err = %v, want ErrReminderCapReached", err)
	}

	count, err := CountActiveReminders(ctx, f.userID)
	if err != nil {
		t.Fatalf("CountActiveReminders: %v", err)
	}
	if count != cap {
		t.Errorf("active count = %d, want exactly the cap %d", count, cap)
	}
}

func TestCreateReminderCapHoldsUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	f := newReminderFixture(t, "cap-race")

	const (
		cap      = 5
		creators = 24
	)

	future := time.Now().Add(time.Hour)
	message := "racing"
	req := pb.CreateReminderReq_builder{
		Datetime:    timestamppb.New(future.UTC()),
		Timezone:    strPtr("Europe/Helsinki"),
		Message:     &message,
		Destination: f.destination,
	}.Build()

	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(),
			`DELETE FROM reminder WHERE user_id = $1`, f.userID); err != nil {
			t.Errorf("cleanup reminders for %s: %v", f.userID, err)
		}
	})

	var (
		mu       sync.Mutex
		accepted int
		start    sync.WaitGroup
		done     sync.WaitGroup
	)
	start.Add(1)
	for i := 0; i < creators; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait() // release together to maximise contention

			_, err := CreateReminder(ctx, req, f.userID, f.destinationID, cap)
			switch err {
			case nil:
				mu.Lock()
				accepted++
				mu.Unlock()
			case ErrReminderCapReached:
			default:
				t.Errorf("CreateReminder: %v", err)
			}
		}()
	}
	start.Done()
	done.Wait()

	if accepted != cap {
		t.Errorf("%d creates were accepted, want exactly the cap %d", accepted, cap)
	}

	count, err := CountActiveReminders(ctx, f.userID)
	if err != nil {
		t.Fatalf("CountActiveReminders: %v", err)
	}
	if count > cap {
		t.Errorf("stored active reminders = %d, which exceeds the cap %d: the check-then-insert race is still open",
			count, cap)
	}
}

// requireOnlyStaleClaims asserts want are the only reminders the table-global
// ReclaimStaleReminders will act on, so a neighbour's leftover stale claim fails
// here by name rather than as an exact-count mismatch elsewhere.
func requireOnlyStaleClaims(t *testing.T, want ...string) {
	t.Helper()

	cutoff := time.Now().UTC().Add(-staleClaimGrace)
	rows, err := db().Query(context.Background(),
		`SELECT id FROM reminder
		  WHERE status = $1 AND deleted = FALSE AND claimed_at <= $2 AND id != ALL($3)`,
		statusOf(pb.ReminderStatus_REMINDER_STATUS_SENT), cutoff, want)
	if err != nil {
		t.Fatalf("look for foreign stale claims: %v", err)
	}
	defer rows.Close()

	var foreign []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan foreign stale claim: %v", err)
		}
		foreign = append(foreign, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign stale claims: %v", err)
	}

	if len(foreign) > 0 {
		t.Fatalf("%d reminder(s) outside this test's fixtures are stale-claimed (%v), so the exact "+
			"ReclaimStaleReminders counts below would be measuring them too; a test that seeds a stale "+
			"claim must clean it up", len(foreign), foreign)
	}
}
