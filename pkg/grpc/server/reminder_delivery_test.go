package server

import (
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func deliveryFor(
	platform pb.Platform,
	reminderID, message, destinationUID, ownerUID string,
) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: platform.Enum(),
		ClientAction: &action,
		ReminderDelivery: pb.ReminderDelivery_builder{
			ReminderId:     &reminderID,
			Message:        &message,
			DestinationUid: &destinationUID,
			OwnerUid:       &ownerUID,
		}.Build(),
	}.Build()
}

// The three optional fields decide where the reminder is posted and who may be mentioned.
func TestReminderNotificationReachesClientWithItsWholeTypedPayload(t *testing.T) {
	h := reverseHarness(t)
	c := openRegisteredReverseClient(t, h, pb.Platform_PLATFORM_DISCORD)

	const (
		reminderID     = "0192f000-0000-7000-8000-000000000001"
		message        = "stand up and stretch"
		destinationUID = "1234567890"
		ownerUID       = "9876543210"
	)
	h.reverseServer.SendAction(deliveryFor(pb.Platform_PLATFORM_DISCORD, reminderID, message, destinationUID, ownerUID))

	waitFor(t, func() bool { return c.rec.count() >= 1 })

	got := c.rec.at(0)

	if got.GetClientAction() != pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION {
		t.Errorf("client action = %v, want SEND_NOTIFICATION", got.GetClientAction())
	}

	// The arm is checked first: every GetX below returns the zero value for the wrong arm.
	if got.WhichPayload() != pb.OpenClientActionStreamResp_ReminderDelivery_case {
		t.Fatalf("payload arm = %v, want reminder_delivery", got.WhichPayload())
	}

	delivery := got.GetReminderDelivery()
	fields := []struct {
		name string
		got  string
		want string
	}{
		{name: "reminder_id", got: delivery.GetReminderId(), want: reminderID},
		{name: "message", got: delivery.GetMessage(), want: message},
		{name: "destination_uid", got: delivery.GetDestinationUid(), want: destinationUID},
		{name: "owner_uid", got: delivery.GetOwnerUid(), want: ownerUID},
	}
	for _, field := range fields {
		if field.got != field.want {
			t.Errorf("delivery %s = %q, want %q", field.name, field.got, field.want)
		}
	}
}

// Presence, not value: the producer sets all four explicitly, flattening NULL columns
// to empty. One that started omitting them would be an otherwise unnoticed wire change.
func TestTheReminderIdIsTheOnlyFieldADeliveryCannotDoWithout(t *testing.T) {
	const reminderID = "0192f000-0000-7000-8000-000000000002"
	delivery := deliveryFor(pb.Platform_PLATFORM_DISCORD, reminderID, "", "", "").GetReminderDelivery()

	if delivery.GetReminderId() != reminderID {
		t.Fatalf("reminder id = %q, want %q", delivery.GetReminderId(), reminderID)
	}

	present := []struct {
		name string
		has  bool
	}{
		{name: "reminder_id", has: delivery.HasReminderId()},
		{name: "message", has: delivery.HasMessage()},
		{name: "destination_uid", has: delivery.HasDestinationUid()},
		{name: "owner_uid", has: delivery.HasOwnerUid()},
	}
	for _, field := range present {
		if !field.has {
			t.Errorf("%s is unset; the producer flattens NULL columns to empty strings, it does not omit them",
				field.name)
		}
	}
}

// A fifth field would be carried for free and ignored, so the descriptor is enumerated.
func TestReminderDeliveryCarriesExactlyTheFourValuesTheClientNeeds(t *testing.T) {
	fields := (&pb.ReminderDelivery{}).ProtoReflect().Descriptor().Fields()

	want := map[protoreflect.FieldNumber]protoreflect.Name{
		1: "reminder_id",
		2: "message",
		3: "destination_uid",
		4: "owner_uid",
	}

	got := make(map[protoreflect.FieldNumber]protoreflect.Name, fields.Len())
	for i := range fields.Len() {
		field := fields.Get(i)
		got[field.Number()] = field.Name()
	}

	if len(got) != len(want) {
		t.Fatalf("ReminderDelivery has %d fields (%v), want %d (%v); the cron producer and the Discord handler both have to learn about a new one",
			len(got), got, len(want), want)
	}
	for number, name := range want {
		if got[number] != name {
			t.Errorf("field %d = %q, want %q", number, got[number], name)
		}
	}
}
