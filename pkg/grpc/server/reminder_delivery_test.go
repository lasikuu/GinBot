package server

import (
	"sync"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/reminder"
)

// The payload key contract is IMPORTED, not restated.
//
// This test used to declare its own "reminder_id"/"message" literals and build
// its own Struct, which made it structurally incapable of detecting drift: a
// rename in pkg/reminder would break every deployed client and leave this test
// green. It now uses reminder.PayloadKey* and, more importantly, the production
// builder reminder.NewDeliveryPayload — the same function pkg/cron/cronjob calls
// — so the thing under test is the real wire payload.
//
// The builder lives in pkg/reminder rather than pkg/cron/cronjob because a test
// in package server cannot import cronjob: cronjob -> pkg/grpc/service -> server
// is an import cycle.

// notificationFor builds the action the server pushes to deliver a reminder,
// using the production payload builder.
func notificationFor(t *testing.T, platform pb.Platform, reminderID, message string) *pb.OpenClientActionStreamResp {
	t.Helper()
	action := pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION
	content := reminder.NewDeliveryPayload(reminderID, message, "", "")
	if content == nil {
		t.Fatal("NewDeliveryPayload returned nil")
	}
	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: platform.Enum(),
		ClientAction: &action,
		Content:      content,
	}.Build()
}

// TestReminderNotificationReachesClientWithIDAndMessage drives the reverse
// stream end to end without Discord: a fake client opens a stream, the server
// pushes a reminder notification through SendAction, and the client must
// receive it with the reminder id and message intact. This is the observable
// half of AC delivery — a confirm would follow, but the payload carrying the id
// and message is what lets the client confirm the right reminder.
func TestReminderNotificationReachesClientWithIDAndMessage(t *testing.T) {
	s := NewReverseServer()
	stream := newFakeStream()

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

	const reminderID = "0192f000-0000-7000-8000-000000000001"
	const message = "stand up and stretch"
	s.SendAction(notificationFor(t, pb.Platform_PLATFORM_DISCORD, reminderID, message))

	waitFor(t, func() bool { return stream.sentCount() >= 1 })

	stream.mu.Lock()
	got := stream.sent[0]
	stream.mu.Unlock()

	if got.GetClientAction() != pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION {
		t.Errorf("client action = %v, want SEND_NOTIFICATION", got.GetClientAction())
	}
	content := got.GetContent()
	if content == nil {
		t.Fatal("notification content is nil; reminder id and message lost")
	}
	if gotID := content.GetFields()[reminder.PayloadKeyReminderID].GetStringValue(); gotID != reminderID {
		t.Errorf("notification reminder id = %q, want %q", gotID, reminderID)
	}
	if gotMsg := content.GetFields()[reminder.PayloadKeyMessage].GetStringValue(); gotMsg != message {
		t.Errorf("notification message = %q, want %q", gotMsg, message)
	}

	close(stream.recv)
	wg.Wait()
}
