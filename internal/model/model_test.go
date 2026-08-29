package model

import (
	"strings"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// Hand-maintained lists: a count mismatch is a runtime scan error.
func TestColumnCountsMatchScanTargets(t *testing.T) {
	tests := []struct {
		name    string
		columns string
		targets int
	}{
		{"User", UserColumns, len((&User{}).ScanTargets())},
		{"Instance", InstanceColumns, len((&Instance{}).ScanTargets())},
		{"Reminder", ReminderColumns, len((&Reminder{}).ScanTargets())},
		{"Trigger", TriggerColumns, len((&Trigger{}).ScanTargets())},
		{"File", FileColumns, len((&File{}).ScanTargets())},
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

func TestScanTargetsAreDistinctPointers(t *testing.T) {
	tests := []struct {
		name    string
		targets []any
	}{
		{"User", (&User{}).ScanTargets()},
		{"Instance", (&Instance{}).ScanTargets()},
		{"Reminder", (&Reminder{}).ScanTargets()},
		{"Trigger", (&Trigger{}).ScanTargets()},
		{"File", (&File{}).ScanTargets()},
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

// Nil optional columns must stay unset, not become empty strings.
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

// GetReminder passes nil when the destination row has been removed.
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

func TestTriggerToProtoMapsEveryFieldAndTheModeEnum(t *testing.T) {
	reply := "a reply"
	fileID := "018f0000-0000-7000-8000-000000000010"
	userID := "018f0000-0000-7000-8000-000000000011"
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := created.Add(time.Hour)

	tr := &Trigger{
		ID:        "018f0000-0000-7000-8000-000000000004",
		Phrase:    "hello",
		Reply:     &reply,
		FileID:    &fileID,
		UserID:    &userID,
		Chance:    42,
		Mode:      int32(pb.TriggerMode_TRIGGER_MODE_EXACT.Number()),
		CreatedAt: created,
		UpdatedAt: updated,
	}

	got := tr.ToProto(nil, nil)

	if got.GetId() != tr.ID {
		t.Errorf("id = %q, want %q", got.GetId(), tr.ID)
	}
	if got.GetPhrase() != tr.Phrase {
		t.Errorf("phrase = %q, want %q", got.GetPhrase(), tr.Phrase)
	}
	if got.GetReply() != reply {
		t.Errorf("reply = %q, want %q", got.GetReply(), reply)
	}
	if got.GetUserId() != userID {
		t.Errorf("user_id = %q, want %q", got.GetUserId(), userID)
	}
	if got.GetChance() != 42 {
		t.Errorf("chance = %d, want 42", got.GetChance())
	}
	if got.GetMode() != pb.TriggerMode_TRIGGER_MODE_EXACT {
		t.Errorf("mode = %v, want TRIGGER_MODE_EXACT", got.GetMode())
	}
	if !got.GetCreatedAt().AsTime().Equal(created) {
		t.Errorf("created_at = %v, want %v", got.GetCreatedAt().AsTime(), created)
	}
	if !got.GetUpdatedAt().AsTime().Equal(updated) {
		t.Errorf("updated_at = %v, want %v", got.GetUpdatedAt().AsTime(), updated)
	}
}

func TestTriggerToProtoToleratesNilFileAndInstances(t *testing.T) {
	tr := &Trigger{
		ID:     "018f0000-0000-7000-8000-000000000005",
		Phrase: "no reply, no file, no instances",
		Chance: 5,
		Mode:   int32(pb.TriggerMode_TRIGGER_MODE_ANY.Number()),
	}

	got := tr.ToProto(nil, nil)

	if got.HasFile() {
		t.Error("file should be unset when nil is passed")
	}
	if len(got.GetInstances()) != 0 {
		t.Errorf("instances = %v, want empty when nil is passed", got.GetInstances())
	}
	if got.HasReply() {
		t.Error("reply should be unset when the column is NULL")
	}
	if got.HasUserId() {
		t.Error("user_id should be unset when the column is NULL")
	}
}

func TestTriggerToProtoCarriesFileAndInstances(t *testing.T) {
	tr := &Trigger{
		ID:     "018f0000-0000-7000-8000-000000000006",
		Phrase: "with attachments",
		Chance: 10,
		Mode:   int32(pb.TriggerMode_TRIGGER_MODE_ANY.Number()),
	}

	fileID := "018f0000-0000-7000-8000-000000000020"
	file := pb.TriggerFile_builder{FileId: &fileID}.Build()

	platform := pb.Platform_PLATFORM_DISCORD
	instance := pb.TriggerInstance_builder{PlatformEnum: &platform}.Build()
	instances := []*pb.TriggerInstance{instance}

	got := tr.ToProto(file, instances)

	if !got.HasFile() {
		t.Fatal("file should be set when supplied")
	}
	if got.GetFile().GetFileId() != fileID {
		t.Errorf("file id = %q, want %q", got.GetFile().GetFileId(), fileID)
	}
	if len(got.GetInstances()) != 1 {
		t.Fatalf("instances = %d entries, want 1", len(got.GetInstances()))
	}
	if got.GetInstances()[0].GetPlatformEnum() != pb.Platform_PLATFORM_DISCORD {
		t.Errorf("instance platform = %v, want PLATFORM_DISCORD", got.GetInstances()[0].GetPlatformEnum())
	}
}

func TestFileToProtoMapsEveryFieldAndUsesTheSuppliedFilename(t *testing.T) {
	f := &File{
		ID:       "018f0000-0000-7000-8000-000000000030",
		Category: 2,
		Path:     "trigger/ab/abc123",
		MimeType: "image/png",
		ByteSize: 12345,
		FileHash: "abc123",
	}

	got := f.ToProto("display-name.png")

	if got.GetFileId() != f.ID {
		t.Errorf("file_id = %q, want %q", got.GetFileId(), f.ID)
	}
	if got.GetFilename() != "display-name.png" {
		t.Errorf("filename = %q, want %q", got.GetFilename(), "display-name.png")
	}
	if got.GetMimeType() != f.MimeType {
		t.Errorf("mime_type = %q, want %q", got.GetMimeType(), f.MimeType)
	}
	if got.GetByteSize() != int64(f.ByteSize) {
		t.Errorf("byte_size = %d, want %d", got.GetByteSize(), f.ByteSize)
	}
}

func TestFileToProtoDifferentFilenamesForTheSameRow(t *testing.T) {
	f := &File{ID: "018f0000-0000-7000-8000-000000000031", MimeType: "image/gif"}

	first := f.ToProto("first.gif")
	second := f.ToProto("second.gif")

	if first.GetFilename() != "first.gif" {
		t.Errorf("first call filename = %q, want %q", first.GetFilename(), "first.gif")
	}
	if second.GetFilename() != "second.gif" {
		t.Errorf("second call filename = %q, want %q", second.GetFilename(), "second.gif")
	}
}
