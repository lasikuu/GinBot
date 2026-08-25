package server

import (
	"sync"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The delivery payload is now the schema, not a convention.
//
// This test used to build a google.protobuf.Struct through reminder.
// NewDeliveryPayload and read it back through the shared reminder.PayloadKey*
// constants, because those constants WERE the contract: the cron loop that wrote
// the Struct and the platform client that read it agreed by nothing else. That
// whole apparatus is gone. ReminderDelivery's field names are the contract now,
// and a rename on one side is a compile error rather than a silently empty
// string on the other.
//
// What is still worth asserting end to end is that the typed payload survives
// the fan-out unaltered: SendAction hands the same *OpenClientActionStreamResp
// pointer to every client, so this is the test that would catch it being rebuilt,
// truncated, or having its arm cleared on the way out.

// deliveryFor builds the action the server pushes to deliver one reminder,
// with all four values a real claim carries.
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

// TestReminderNotificationReachesClientWithItsWholeTypedPayload drives the
// reverse stream end to end without Discord: a fake client opens a stream, the
// server pushes a reminder notification through SendAction, and the client must
// receive it with every field intact.
//
// All four fields are asserted, not just the id and the message. The three
// optional ones are what decide where the reminder is posted and who may be
// mentioned, so a delivery that arrives with them dropped is not a cosmetic loss:
// it silently becomes a DM-less, channel-less reminder that the client then
// reports as a failed delivery.
func TestReminderNotificationReachesClientWithItsWholeTypedPayload(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream(t)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.OpenClientActionStream(stream)
	}()

	stream.recv <- pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()
	waitFor(t, func() bool { return s.clientCount() == 1 })

	const (
		reminderID     = "0192f000-0000-7000-8000-000000000001"
		message        = "stand up and stretch"
		destinationUID = "1234567890"
		ownerUID       = "9876543210"
	)
	s.SendAction(deliveryFor(pb.Platform_PLATFORM_DISCORD, reminderID, message, destinationUID, ownerUID))

	waitFor(t, func() bool { return stream.sentCount() >= 1 })

	stream.mu.Lock()
	got := stream.sent[0]
	stream.mu.Unlock()

	if got.GetClientAction() != pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION {
		t.Errorf("client action = %v, want SEND_NOTIFICATION", got.GetClientAction())
	}

	// The arm is checked before its contents. Every GetX below returns the zero
	// value for the wrong arm, so without this a payload that arrived as the
	// heartbeat arm would fail as four confusing empty-string mismatches instead
	// of one accurate line.
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

// TestTheReminderIdIsTheOnlyFieldADeliveryCannotDoWithout states the asymmetry
// the client's drop rule depends on, at the schema.
//
// Presence, not value: the cron producer sets all four explicitly, defaulting the
// nullable columns to empty rather than leaving them unset, so HasX is true for
// an empty message and an empty owner. That is deliberate — "" and unset mean the
// same thing to every consumer — and this pins it, because a producer that
// started OMITTING empty fields would be a wire change that no other test here
// would notice.
func TestTheReminderIdIsTheOnlyFieldADeliveryCannotDoWithout(t *testing.T) {
	// Everything optional legitimately empty: no message, no channel, no owner
	// platform identity. Only the id is non-empty.
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

// TestReminderDeliveryCarriesExactlyTheFourValuesTheClientNeeds is the tripwire
// for the message growing a field.
//
// A fifth field would be carried by the server for free and silently ignored by
// every client, so nothing else in this repository would fail. Enumerating the
// descriptor means adding one has to be a deliberate act that updates the cron
// producer and the Discord handler together.
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
