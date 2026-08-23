package model

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
)

// structWithNumberField builds a *structpb.Struct with one key set to a
// numeric value instead of a string, to exercise the "malformed field" decode
// path.
func structWithNumberField(t *testing.T, key string, value float64) *structpb.Struct {
	t.Helper()
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			key: structpb.NewNumberValue(value),
		},
	}
}

// ── Assumed symbols from internal/model (spec §3.5) ───────────────────────────
//
// Recorded because these are the symbols the tests below depend on, so a change
// to any of them is a deliberate decision rather than a surprise.
//
//	const (
//		RefFieldInstanceUID    = "instance_uid"
//		RefFieldDestinationUID = "destination_uid"
//		RefFieldMessageUID     = "message_uid"
//		RefFieldAuthorUID      = "author_uid"
//	)
//
//	type RepostEntry struct {
//		ID            int64
//		InstanceID    int64
//		DestinationID *int64
//		UserID        *string
//		Kind          int32
//		SourceKey     *string
//		CanonicalURL  *string
//		FileID        *string
//		ContentHash   []byte
//		MsgRef        *structpb.Struct
//		PostedAt      time.Time
//		CreatedAt     time.Time
//		UpdatedAt     time.Time
//	}
//
//	const RepostEntryColumns = `...`
//	func (r *RepostEntry) ScanTargets() []any
//	func (r *RepostEntry) MessageRef() *pb.MessageRef
//	func NewRepostMsgRef(instanceUID, destinationUID, messageUID, authorUID string) *structpb.Struct

// TestRepostEntryColumnCountMatchesScanTargets mirrors
// TestColumnCountsMatchScanTargets in model_test.go, scoped to RepostEntry: a
// count mismatch between RepostEntryColumns and ScanTargets produces a runtime
// scan error, and a same-count reordering of two same-typed fields silently
// loads the wrong column into the wrong field.
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

// TestRepostEntryScanTargetsAreDistinctPointers mirrors
// TestScanTargetsAreDistinctPointers: pgx must be able to write into every
// target, and no two columns may alias the same field.
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

// ── MessageRef ────────────────────────────────────────────────────────────────

// TestNewRepostMsgRefRoundTripsThroughMessageRef: building msg_ref with
// NewRepostMsgRef and reading it back with MessageRef must reproduce every
// field exactly. This is the round trip the deep link and the "@author"
// mention in WANHA's notification both depend on.
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

// TestMessageRefHandlesANilStruct: a row is NOT NULL on msg_ref per the schema,
// but a defensively-nil field (e.g. a struct built by a test or a future
// caller that forgot to set it) must not panic — it must degrade to an empty
// MessageRef rather than crash whatever renders the WANHA notification.
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

// TestMessageRefTreatsAMissingFieldAsEmptyNotAnError covers a msg_ref that is
// missing one of the four documented keys entirely — e.g. an entry stored by a
// platform that does not carry a destination — degrading gracefully per spec
// rather than panicking or fabricating a value.
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

// TestMessageRefTreatsAMalformedFieldAsEmptyNotAnError: a jsonb value that is
// present but not a string (e.g. a stray number under a documented key) must
// not panic and must not be coerced into a string representation of the wrong
// type — the spec is explicit that this yields an empty string, not an error.
func TestMessageRefTreatsAMalformedFieldAsEmptyNotAnError(t *testing.T) {
	entry := &RepostEntry{MsgRef: structWithNumberField(t, RefFieldInstanceUID, 12345)}

	got := entry.MessageRef()
	if got.GetInstanceUid() != "" {
		t.Errorf("InstanceUid = %q for a non-string jsonb value, want empty", got.GetInstanceUid())
	}
}

// TestRepostEntryTimestampFieldsAreNotStructpb sanity-checks the field types
// documented for RepostEntry stay wall-clock times, not protobuf timestamps —
// consistent with every other model in this package (User, Reminder, ...).
func TestRepostEntryTimestampFieldsAreNotStructpb(t *testing.T) {
	now := time.Now()
	entry := &RepostEntry{PostedAt: now, CreatedAt: now, UpdatedAt: now}

	if !entry.PostedAt.Equal(now) || !entry.CreatedAt.Equal(now) || !entry.UpdatedAt.Equal(now) {
		t.Error("RepostEntry timestamp fields did not round-trip a time.Time value")
	}
}
