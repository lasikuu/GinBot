package model

import (
	"strings"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// TestTriggerToProtoCarriesTheRef: the short ref is a separate column from id,
// and must round-trip through ToProto onto Trigger.ref (field 12).
func TestTriggerToProtoCarriesTheRef(t *testing.T) {
	tr := &Trigger{
		ID:     "018f0000-0000-7000-8000-000000000040",
		Phrase: "ref-roundtrip",
		Chance: 10,
		Mode:   int32(pb.TriggerMode_TRIGGER_MODE_ANY.Number()),
		Ref:    42,
	}

	got := tr.ToProto(nil, nil)

	if got.GetRef() != 42 {
		t.Errorf("GetRef() = %d, want 42", got.GetRef())
	}
	// The UUID must still be present: formatTriggerInfo needs both.
	if got.GetId() != tr.ID {
		t.Errorf("GetId() = %q, want %q", got.GetId(), tr.ID)
	}
}

func TestReminderToProtoCarriesTheRef(t *testing.T) {
	message := "ref-roundtrip"
	r := &Reminder{
		ID:       "018f0000-0000-7000-8000-000000000041",
		Datetime: time.Now(),
		Timezone: "UTC",
		Status:   int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
		Message:  &message,
		Ref:      7,
	}

	got := r.ToProto(nil)

	if got.GetRef() != 7 {
		t.Errorf("GetRef() = %d, want 7", got.GetRef())
	}
	if got.GetId() != r.ID {
		t.Errorf("GetId() = %q, want %q", got.GetId(), r.ID)
	}
}

// TestTriggerColumnsCarriesTheRefColumn: TriggerColumns must list the new
// column, so ScanTargets (extended by production to add &t.Ref) stays aligned
// with the SELECT list. TestColumnCountsMatchScanTargets already fails when the
// counts drift; this pins that "ref" specifically is one of the columns.
func TestTriggerColumnsCarriesTheRefColumn(t *testing.T) {
	found := false
	for _, c := range strings.Split(TriggerColumns, ",") {
		if strings.TrimSpace(c) == "ref" {
			found = true
		}
	}
	if !found {
		t.Errorf("TriggerColumns = %q, does not list a ref column", TriggerColumns)
	}
}

func TestReminderColumnsCarriesTheRefColumn(t *testing.T) {
	found := false
	for _, c := range strings.Split(ReminderColumns, ",") {
		if strings.TrimSpace(c) == "ref" {
			found = true
		}
	}
	if !found {
		t.Errorf("ReminderColumns = %q, does not list a ref column", ReminderColumns)
	}
}

// TestTriggerRefScansIntoItsOwnField: ScanTargets must expose &t.Ref as one of
// its pointers, distinct from every other field, or a scan would silently write
// the ref into the wrong column.
func TestTriggerRefScansIntoItsOwnField(t *testing.T) {
	tr := &Trigger{}
	targets := tr.ScanTargets()

	found := false
	for _, target := range targets {
		if refPtr, ok := target.(*int64); ok && refPtr == &tr.Ref {
			found = true
		}
	}
	if !found {
		t.Error("ScanTargets() does not expose &Trigger.Ref as a distinct scan target")
	}
}

func TestReminderRefScansIntoItsOwnField(t *testing.T) {
	r := &Reminder{}
	targets := r.ScanTargets()

	found := false
	for _, target := range targets {
		if refPtr, ok := target.(*int64); ok && refPtr == &r.Ref {
			found = true
		}
	}
	if !found {
		t.Error("ScanTargets() does not expose &Reminder.Ref as a distinct scan target")
	}
}
