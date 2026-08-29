package model

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
)

// structWithNumberField exercises the "malformed field" decode path.
func structWithNumberField(t *testing.T, key string, value float64) *structpb.Struct {
	t.Helper()
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			key: structpb.NewNumberValue(value),
		},
	}
}

func TestRepostEntryColumnCountMatchesScanTargets(t *testing.T) {
	columns := strings.Split(RepostEntryColumns, ",")
	targets := (&RepostEntry{}).ScanTargets()

	if len(columns) != len(targets) {
		t.Fatalf("RepostEntryColumns has %d columns but ScanTargets returns %d targets", len(columns), len(targets))
	}
	for i, c := range columns {
		if strings.TrimSpace(c) == "" {
			t.Errorf("RepostEntryColumns[%d] is empty in %q", i, RepostEntryColumns)
		}
	}
}

// No two columns may alias the same field.
func TestRepostEntryScanTargetsAreDistinctPointers(t *testing.T) {
	targets := (&RepostEntry{}).ScanTargets()

	seen := make(map[any]bool, len(targets))
	for i, target := range targets {
		if target == nil {
			t.Fatalf("scan target %d is nil", i)
		}
		if seen[target] {
			t.Errorf("scan target %d is a duplicate; two columns would write the same field", i)
		}
		seen[target] = true
	}
}

func TestNewRepostMsgRefRoundTripsThroughMessageRef(t *testing.T) {
	msgRef := NewRepostMsgRef("instance-1", "destination-2", "message-3", "author-4")

	entry := &RepostEntry{MsgRef: msgRef}
	got := entry.MessageRef()

	if got == nil {
		t.Fatal("MessageRef() returned nil for a populated msg_ref")
	}
	if got.GetInstanceUid() != "instance-1" {
		t.Errorf("InstanceUid = %q, want %q", got.GetInstanceUid(), "instance-1")
	}
	if got.GetDestinationUid() != "destination-2" {
		t.Errorf("DestinationUid = %q, want %q", got.GetDestinationUid(), "destination-2")
	}
	if got.GetMessageUid() != "message-3" {
		t.Errorf("MessageUid = %q, want %q", got.GetMessageUid(), "message-3")
	}
	if got.GetAuthorUid() != "author-4" {
		t.Errorf("AuthorUid = %q, want %q", got.GetAuthorUid(), "author-4")
	}
}

// msg_ref is NOT NULL in the schema, but a nil struct must not panic.
func TestMessageRefHandlesANilStruct(t *testing.T) {
	entry := &RepostEntry{MsgRef: nil}

	got := entry.MessageRef()
	if got == nil {
		t.Fatal("MessageRef() returned nil for a nil msg_ref; want a usable, empty MessageRef")
	}
	if got.GetInstanceUid() != "" || got.GetDestinationUid() != "" || got.GetMessageUid() != "" || got.GetAuthorUid() != "" {
		t.Errorf("MessageRef() for a nil msg_ref = %+v, want every field empty", got)
	}
}

func TestMessageRefTreatsAMissingFieldAsEmptyNotAnError(t *testing.T) {
	partial := NewRepostMsgRef("instance-only", "", "message-only", "")

	entry := &RepostEntry{MsgRef: partial}
	got := entry.MessageRef()

	if got.GetInstanceUid() != "instance-only" {
		t.Errorf("InstanceUid = %q, want %q", got.GetInstanceUid(), "instance-only")
	}
	if got.GetMessageUid() != "message-only" {
		t.Errorf("MessageUid = %q, want %q", got.GetMessageUid(), "message-only")
	}
	if got.GetDestinationUid() != "" {
		t.Errorf("DestinationUid = %q, want empty for a field that was never set", got.GetDestinationUid())
	}
	if got.GetAuthorUid() != "" {
		t.Errorf("AuthorUid = %q, want empty for a field that was never set", got.GetAuthorUid())
	}
}

func TestMessageRefTreatsAMalformedFieldAsEmptyNotAnError(t *testing.T) {
	entry := &RepostEntry{MsgRef: structWithNumberField(t, RefFieldInstanceUID, 12345)}

	got := entry.MessageRef()
	if got.GetInstanceUid() != "" {
		t.Errorf("InstanceUid = %q for a non-string jsonb value, want empty", got.GetInstanceUid())
	}
}

func TestRepostEntryTimestampFieldsAreNotStructpb(t *testing.T) {
	now := time.Now()
	entry := &RepostEntry{PostedAt: now, CreatedAt: now, UpdatedAt: now}

	if !entry.PostedAt.Equal(now) || !entry.CreatedAt.Equal(now) || !entry.UpdatedAt.Equal(now) {
		t.Error("RepostEntry timestamp fields did not round-trip a time.Time value")
	}
}
