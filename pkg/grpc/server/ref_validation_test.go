package server

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// idPatternCases is shared by every relaxed id field: ExecTriggerReq,
// GetTriggerReq, UpdateTriggerReq, DeleteTriggerReq, GetReminderReq,
// UpdateReminderReq and DeleteReminderReq all accept the same shape (ADR-0039):
// a UUID in either case, or a positive decimal ref. Either case, because the
// string.uuid rule these replaced accepted both and relaxing a rule must not
// narrow it.
var idPatternCases = []struct {
	name    string
	id      string
	accepts bool
}{
	{name: "a positive decimal ref", id: "12", accepts: true},
	{name: "ref of exactly one digit", id: "1", accepts: true},
	{name: "a canonical lowercase uuid", id: "018f0000-0000-7000-8000-000000000001", accepts: true},
	{name: "an uppercase uuid", id: "018F0000-0000-7000-8000-000000000001", accepts: true},
	{name: "zero is not a positive ref", id: "0", accepts: false},
	{name: "a negative number", id: "-1", accepts: false},
	{name: "a zero-padded ref", id: "0123", accepts: false},
	{name: "not a number or a uuid", id: "abc", accepts: false},
	{name: "empty", id: "", accepts: false},
	{name: "a braced uuid", id: "{018f0000-0000-7000-8000-000000000001}", accepts: false},
}

// requireSchemaOutcome runs one id through a validate-only assertion: the
// schema either refuses it as InvalidArgument before any resolver runs, or
// lets it through to the unregistered-caller floor (FailedPrecondition). This
// isolates the pattern rule from clearance and ownership, exactly like
// TestMalformedTriggerIdIsRejectedByTheChain does for the id-only case.
func requireSchemaOutcome(t *testing.T, accepts bool, err error) {
	t.Helper()

	if accepts {
		requireCode(t, err, connect.CodeFailedPrecondition)
		return
	}
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestGetTriggerAcceptsRefOrUUIDShapeOnly(t *testing.T) {
	h := newHarness(t)

	for _, tt := range idPatternCases {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			_, err := h.Trigger.GetTrigger(unregisteredCtx(), pb.GetTriggerReq_builder{Id: &id}.Build())
			requireSchemaOutcome(t, tt.accepts, err)
		})
	}
}

func TestDeleteTriggerAcceptsRefOrUUIDShapeOnly(t *testing.T) {
	h := newHarness(t)

	for _, tt := range idPatternCases {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			_, err := h.Trigger.DeleteTrigger(unregisteredCtx(), pb.DeleteTriggerReq_builder{Id: &id}.Build())
			requireSchemaOutcome(t, tt.accepts, err)
		})
	}
}

func TestUpdateTriggerAcceptsRefOrUUIDShapeOnly(t *testing.T) {
	h := newHarness(t)

	for _, tt := range idPatternCases {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			_, err := h.Trigger.UpdateTrigger(unregisteredCtx(), pb.UpdateTriggerReq_builder{Id: &id}.Build())
			requireSchemaOutcome(t, tt.accepts, err)
		})
	}
}

func TestExecTriggerAcceptsRefOrUUIDShapeOnly(t *testing.T) {
	h := newHarness(t)

	for _, tt := range idPatternCases {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			_, err := h.Trigger.ExecTrigger(unregisteredCtx(), pb.ExecTriggerReq_builder{
				Id:       &id,
				Instance: harnessInstances(t, 1)[0],
			}.Build())
			requireSchemaOutcome(t, tt.accepts, err)
		})
	}
}

func TestGetReminderAcceptsRefOrUUIDShapeOnly(t *testing.T) {
	h := newHarness(t)

	for _, tt := range idPatternCases {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			_, err := h.Reminder.GetReminder(unregisteredCtx(), pb.GetReminderReq_builder{Id: &id}.Build())
			requireSchemaOutcome(t, tt.accepts, err)
		})
	}
}

func TestDeleteReminderAcceptsRefOrUUIDShapeOnly(t *testing.T) {
	h := newHarness(t)

	for _, tt := range idPatternCases {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			_, err := h.Reminder.DeleteReminder(unregisteredCtx(), pb.DeleteReminderReq_builder{Id: &id}.Build())
			requireSchemaOutcome(t, tt.accepts, err)
		})
	}
}

// validReminderDestination is otherwise-valid so only the id field under test
// can be responsible for a schema rejection.
func validReminderDestination(t *testing.T) *pb.ReminderDestination {
	t.Helper()

	instanceMeta, err := structpb.NewStruct(map[string]any{"instance_uid": "guild-1"})
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	destinationMeta, err := structpb.NewStruct(map[string]any{"destination_uid": "chan-1"})
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	platform := pb.Platform_PLATFORM_DISCORD

	return pb.ReminderDestination_builder{
		PlatformEnum:    &platform,
		InstanceMeta:    instanceMeta,
		DestinationMeta: destinationMeta,
	}.Build()
}

func TestUpdateReminderAcceptsRefOrUUIDShapeOnly(t *testing.T) {
	h := newHarness(t)
	future := timestamppb.New(time.Now().Add(time.Hour))

	for _, tt := range idPatternCases {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			_, err := h.Reminder.UpdateReminder(unregisteredCtx(), pb.UpdateReminderReq_builder{
				Id:          &id,
				Datetime:    future,
				Destination: validReminderDestination(t),
			}.Build())
			requireSchemaOutcome(t, tt.accepts, err)
		})
	}
}

// TestReminderParentIDStillRequiresACanonicalUUID: parent_id is not one of the
// relaxed fields, so a ref-shaped value must still be refused there.
func TestReminderParentIDStillRequiresACanonicalUUID(t *testing.T) {
	h := newHarness(t)
	future := timestamppb.New(time.Now().Add(time.Hour))
	tz := "UTC"
	message := "parent id validation"

	tests := []struct {
		name     string
		parentID string
		accepts  bool
	}{
		{name: "a ref-shaped value is refused for parent_id", parentID: "12", accepts: false},
		{name: "a canonical uuid is accepted", parentID: "018f0000-0000-7000-8000-000000000001", accepts: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parentID := tt.parentID
			_, err := h.Reminder.CreateReminder(unregisteredCtx(), pb.CreateReminderReq_builder{
				Datetime:    future,
				Timezone:    &tz,
				Message:     &message,
				Destination: validReminderDestination(t),
				ParentId:    &parentID,
			}.Build())
			requireSchemaOutcome(t, tt.accepts, err)
		})
	}
}
