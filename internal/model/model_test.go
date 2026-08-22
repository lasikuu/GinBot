package model

import (
	"strings"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

// The column lists and the ScanTargets slices are maintained by hand and must
// stay in lockstep. A count mismatch produces a scan error at runtime; worse, a
// same-count reordering of two same-typed columns (User has three *string
// fields in a row) silently loads wrong data. This at least pins the count.
func TestColumnCountsMatchScanTargets(t *testing.T) {
	tests := []struct {
		name    string
		columns string
		targets int
	}{
		{"User", UserColumns, len((&User{}).ScanTargets())},
		{"Instance", InstanceColumns, len((&Instance{}).ScanTargets())},
		{"Reminder", ReminderColumns, len((&Reminder{}).ScanTargets())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns := strings.Split(tt.columns, ",")
			if len(columns) != tt.targets {
				t.Errorf("%s: %d columns but %d scan targets", tt.name, len(columns), tt.targets)
			}
			for _, c := range columns {
				if strings.TrimSpace(c) == "" {
					t.Errorf("%s: empty column name in %q", tt.name, tt.columns)
				}
			}
		})
	}
}

// Scan targets must all be pointers, or pgx cannot write into them.
func TestScanTargetsAreDistinctPointers(t *testing.T) {
	tests := []struct {
		name    string
		targets []any
	}{
		{"User", (&User{}).ScanTargets()},
		{"Instance", (&Instance{}).ScanTargets()},
		{"Reminder", (&Reminder{}).ScanTargets()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := make(map[any]bool, len(tt.targets))
			for i, target := range tt.targets {
				if target == nil {
					t.Fatalf("scan target %d is nil", i)
				}
				if seen[target] {
					t.Errorf("scan target %d is a duplicate; two columns would write the same field", i)
				}
				seen[target] = true
			}
		})
	}
}

// ToProto must not panic on a zero value, and nil optional columns must stay
// unset rather than materialising as empty strings.
func TestUserToProtoHandlesNilOptionals(t *testing.T) {
	u := &User{
		ID:        "018f0000-0000-7000-8000-000000000001",
		Username:  "someone",
		Clearance: int32(pb.Clearance_CLEARANCE_REGISTERED.Number()),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	got := u.ToProto()

	if got.GetId() != u.ID {
		t.Errorf("id = %q, want %q", got.GetId(), u.ID)
	}
	if got.HasAvatar() {
		t.Error("avatar should be unset when the column is NULL")
	}
	if got.HasLocale() {
		t.Error("locale should be unset when the column is NULL")
	}
	if got.HasTimezone() {
		t.Error("timezone should be unset when the column is NULL")
	}
	if got.HasBirthday() {
		t.Error("birthday should be unset when the column is NULL")
	}
	if got.HasLastCongratulatedAt() {
		t.Error("last_congratulated_at should be unset when the column is NULL")
	}
	if !got.HasCreatedAt() || !got.HasUpdatedAt() {
		t.Error("created_at and updated_at are NOT NULL and must always be set")
	}
}

func TestUserToProtoCarriesOptionals(t *testing.T) {
	avatar, locale, timezone := "abc123", "fi", "Europe/Helsinki"
	birthday := time.Date(1990, 5, 17, 8, 0, 0, 0, time.UTC)

	u := &User{
		ID:        "018f0000-0000-7000-8000-000000000002",
		Username:  "someone",
		Clearance: int32(pb.Clearance_CLEARANCE_ADMINISTRATOR.Number()),
		Avatar:    &avatar,
		Locale:    &locale,
		Timezone:  &timezone,
		Birthday:  &birthday,
	}

	got := u.ToProto()

	if got.GetAvatar() != avatar {
		t.Errorf("avatar = %q, want %q", got.GetAvatar(), avatar)
	}
	if got.GetLocale() != locale {
		t.Errorf("locale = %q, want %q", got.GetLocale(), locale)
	}
	if got.GetTimezone() != timezone {
		t.Errorf("timezone = %q, want %q", got.GetTimezone(), timezone)
	}
	if !got.GetBirthday().AsTime().Equal(birthday) {
		t.Errorf("birthday = %v, want %v", got.GetBirthday().AsTime(), birthday)
	}
	if got.GetClearance() != pb.Clearance_CLEARANCE_ADMINISTRATOR {
		t.Errorf("clearance = %v, want ADMINISTRATOR", got.GetClearance())
	}
}

// Reminder.ToProto takes the destination as an argument, so it must tolerate nil
// — GetReminder passes nil when the destination row has been removed, treating
// db.ErrNotFound from the destination lookup as "no destination" rather than as a
// failure.
func TestReminderToProtoHandlesNilDestination(t *testing.T) {
	message := "water the plants"
	r := &Reminder{
		ID:            "018f0000-0000-7000-8000-000000000003",
		Datetime:      time.Now(),
		Timezone:      "UTC",
		DestinationID: 1,
		Status:        int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
		Message:       &message,
	}

	got := r.ToProto(nil)

	if got.HasDestination() {
		t.Error("destination should be unset when nil is passed")
	}
	if got.HasRepeatCron() {
		t.Error("repeat_cron should be unset when the column is NULL")
	}
	if got.HasParentId() {
		t.Error("parent_id should be unset when the column is NULL")
	}
	if got.HasUserId() {
		t.Error("user_id should be unset when the column is NULL")
	}
	if got.GetMessage() != message {
		t.Errorf("message = %q, want %q", got.GetMessage(), message)
	}
	if got.GetStatus() != pb.ReminderStatus_REMINDER_STATUS_PENDING {
		t.Errorf("status = %v, want PENDING", got.GetStatus())
	}
}

func TestInstanceToProtoHandlesNilDefaultChannel(t *testing.T) {
	i := &Instance{
		ID:           7,
		PlatformEnum: int32(pb.Platform_PLATFORM_DISCORD.Number()),
	}

	got := i.ToProto()

	if got.GetId() != 7 {
		t.Errorf("id = %d, want 7", got.GetId())
	}
	if got.GetPlatformEnum() != pb.Platform_PLATFORM_DISCORD {
		t.Errorf("platform = %v, want DISCORD", got.GetPlatformEnum())
	}
	if got.HasDefaultChannel() {
		t.Error("default_channel should be unset when the column is NULL")
	}
}
