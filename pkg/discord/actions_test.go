package discord

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// log.Z is nil until initialised. discordSession is left nil on purpose: it is
// the seam that proves a branch never touches Discord, by panicking if it does.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// A nil delivery leaves the oneof arm unset.
func notificationAction(delivery *pb.ReminderDelivery) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum:     pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction:     &action,
		ReminderDelivery: delivery,
	}.Build()
}

func heartbeatAction(test *pb.TestAction) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &action,
		Test:         test,
	}.Build()
}

// A SEND_NOTIFICATION whose payload is the wrong arm.
func notificationCarryingTheTestArm() *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &action,
		Test:         pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build(),
	}.Build()
}

// Field numbers, because WhichPayload's generated return type is unexported and
// cannot be named from another package.
var (
	payloadNotSet           = protoreflect.FieldNumber(pb.OpenClientActionStreamResp_Payload_not_set_case)
	payloadReminderDelivery = protoreflect.FieldNumber(pb.OpenClientActionStreamResp_ReminderDelivery_case)
	payloadTest             = protoreflect.FieldNumber(pb.OpenClientActionStreamResp_Test_case)
)

func payloadCase(in *pb.OpenClientActionStreamResp) protoreflect.FieldNumber {
	return protoreflect.FieldNumber(in.WhichPayload())
}

func delivery(reminderID, message, destinationUID, ownerUID string) *pb.ReminderDelivery {
	return pb.ReminderDelivery_builder{
		ReminderId:     &reminderID,
		Message:        &message,
		DestinationUid: &destinationUID,
		OwnerUid:       &ownerUID,
	}.Build()
}

func TestReminderDeliveryAcceptsOnlyItsOwnArm(t *testing.T) {
	tests := []struct {
		name     string
		in       *pb.OpenClientActionStreamResp
		wantCase protoreflect.FieldNumber
		wantNil  bool
	}{
		{
			name:     "no arm set at all",
			in:       notificationAction(nil),
			wantCase: payloadNotSet,
			wantNil:  true,
		},
		{
			name:     "the heartbeat arm on a notification",
			in:       notificationCarryingTheTestArm(),
			wantCase: payloadTest,
			wantNil:  true,
		},
		{
			name:     "its own arm",
			in:       notificationAction(delivery("id-1", "stretch", "chan-1", "owner-1")),
			wantCase: payloadReminderDelivery,
			wantNil:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := payloadCase(tt.in); got != tt.wantCase {
				t.Fatalf("WhichPayload() = %v (field %d), want field %d",
					tt.in.WhichPayload(), got, tt.wantCase)
			}

			got := reminderDelivery(tt.in)
			if (got == nil) != tt.wantNil {
				t.Fatalf("reminderDelivery() = %v, want nil == %v", got, tt.wantNil)
			}
			if tt.wantNil {
				return
			}
			if got.GetReminderId() != "id-1" {
				t.Errorf("reminder id = %q, want %q", got.GetReminderId(), "id-1")
			}
		})
	}
}

// TestThePayloadArmsAreExactlyTheOnesTheSwitchHandles makes a new oneof arm fail
// here rather than go unhandled in reminderDelivery.
func TestThePayloadArmsAreExactlyTheOnesTheSwitchHandles(t *testing.T) {
	payload := (&pb.OpenClientActionStreamResp{}).ProtoReflect().Descriptor().Oneofs().ByName("payload")
	if payload == nil {
		t.Fatal("OpenClientActionStreamResp has no oneof named payload")
	}

	want := map[protoreflect.FieldNumber]protoreflect.Name{
		4: "reminder_delivery",
		5: "test",
	}

	got := make(map[protoreflect.FieldNumber]protoreflect.Name, payload.Fields().Len())
	for i := range payload.Fields().Len() {
		field := payload.Fields().Get(i)
		got[field.Number()] = field.Name()
	}

	if len(got) != len(want) {
		t.Fatalf("payload has %d arms (%v), want %d (%v); reminderDelivery's switch has to handle each one",
			len(got), got, len(want), want)
	}
	for number, name := range want {
		if got[number] != name {
			t.Errorf("payload arm %d = %q, want %q", number, got[number], name)
		}
	}
}

// The Reminder field is left a nil interface, so any ConfirmDelivery panics.
func grpcClientsWithNoReminder(ctx context.Context) context.Context {
	return withClients(ctx, &client.Clients{})
}

// Must not panic, post or confirm: a nil discordSession and a nil Reminder
// interface make any of those a failure rather than a silent pass.
func TestHandleSendNotificationDropsUnusableActionsWithoutPostingOrConfirming(t *testing.T) {
	if discordSession != nil {
		t.Fatal("discordSession must stay nil for this test to mean anything")
	}

	ctx := grpcClientsWithNoReminder(context.Background())
	if clientsFrom(ctx).Reminder != nil {
		t.Fatal("Reminder must stay a nil interface for this test to mean anything")
	}

	tests := []struct {
		name string
		in   *pb.OpenClientActionStreamResp
	}{
		{
			name: "no payload arm set",
			in:   notificationAction(nil),
		},
		{
			name: "the heartbeat arm instead of a delivery",
			in:   notificationCarryingTheTestArm(),
		},
		{
			name: "a delivery with no fields set at all",
			in:   notificationAction(pb.ReminderDelivery_builder{}.Build()),
		},
		{
			name: "a delivery with an empty reminder id",
			in:   notificationAction(delivery("", "orphan", "chan-1", "owner-1")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handleSendNotification(ctx, tt.in)
		})
	}
}

// TestHandleSendTestSurvivesEveryPayloadArm: the heartbeat handler reads through
// two levels of optional message, so a nil deref on either must not happen.
func TestHandleSendTestSurvivesEveryPayloadArm(t *testing.T) {
	tests := []struct {
		name string
		in   *pb.OpenClientActionStreamResp
	}{
		{
			name: "no payload arm set",
			in:   heartbeatAction(nil),
		},
		{
			// GetEmittedAt returns nil and AsTime is called on it.
			name: "the heartbeat arm with no emission time",
			in:   heartbeatAction(pb.TestAction_builder{}.Build()),
		},
		{
			name: "a fully populated heartbeat",
			in:   heartbeatAction(pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build()),
		},
		{
			name: "a reminder delivery on a heartbeat action",
			in: func() *pb.OpenClientActionStreamResp {
				action := pb.ClientAction_CLIENT_ACTION_SEND_TEST
				return pb.OpenClientActionStreamResp_builder{
					PlatformEnum:     pb.Platform_PLATFORM_DISCORD.Enum(),
					ClientAction:     &action,
					ReminderDelivery: delivery("id-1", "stretch", "chan-1", "owner-1"),
				}.Build()
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handleSendTest(context.Background(), tt.in)
		})
	}
}

func TestDeliveryPlanOrdersChannelBeforeDirectMessage(t *testing.T) {
	tests := []struct {
		name      string
		delivery  *pb.ReminderDelivery
		wantRoute []deliveryRoute
	}{
		{
			name:     "channel and owner: channel first, DM as fallback",
			delivery: delivery("id-1", "stretch", "chan-1", "owner-1"),
			wantRoute: []deliveryRoute{
				{Kind: routeChannel, Target: "chan-1"},
				{Kind: routeDirectMessage, Target: "owner-1"},
			},
		},
		{
			name:      "no channel: DM is the only route",
			delivery:  delivery("id-1", "stretch", "", "owner-1"),
			wantRoute: []deliveryRoute{{Kind: routeDirectMessage, Target: "owner-1"}},
		},
		{
			name:      "no owner: channel only, no fallback",
			delivery:  delivery("id-1", "stretch", "chan-1", ""),
			wantRoute: []deliveryRoute{{Kind: routeChannel, Target: "chan-1"}},
		},
		{
			name:      "neither: give up",
			delivery:  delivery("id-1", "stretch", "", ""),
			wantRoute: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deliveryPlan(tt.delivery.GetDestinationUid(), tt.delivery.GetOwnerUid())
			if len(got) != len(tt.wantRoute) {
				t.Fatalf("plan = %+v, want %+v", got, tt.wantRoute)
			}
			for i := range tt.wantRoute {
				if got[i] != tt.wantRoute[i] {
					t.Errorf("plan[%d] = %+v, want %+v", i, got[i], tt.wantRoute[i])
				}
			}
		})
	}
}

// The one postNotification path reachable without a session: an empty plan
// returns before any send.
func TestPostNotificationReportsAFailedDeliveryWhenThereIsNoRoute(t *testing.T) {
	if discordSession != nil {
		t.Fatal("discordSession must stay nil for this test to mean anything")
	}

	if postNotification(delivery("id-1", "stretch", "", "")) {
		t.Error("postNotification reported a delivery for a reminder with no channel and no owner")
	}
}

func TestChannelNotificationMentionsTheOwner(t *testing.T) {
	tests := []struct {
		name     string
		delivery *pb.ReminderDelivery
		want     string
	}{
		{
			name:     "owner mention prefixes the body",
			delivery: delivery("id-1", "stretch", "chan-1", "123"),
			want:     "<@123> stretch",
		},
		{
			name:     "empty message still notifies",
			delivery: delivery("id-1", "", "chan-1", "123"),
			want:     "<@123> " + emptyReminderBody,
		},
		{
			name:     "no owner: body only, no broken mention",
			delivery: delivery("id-1", "stretch", "chan-1", ""),
			want:     "stretch",
		},
		{
			name:     "no owner and no message",
			delivery: delivery("id-1", "", "chan-1", ""),
			want:     emptyReminderBody,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := channelNotification(tt.delivery.GetMessage(), tt.delivery.GetOwnerUid())
			if got != tt.want {
				t.Errorf("channelNotification = %q, want %q", got, tt.want)
			}
		})
	}
}

// A body just under the limit plus a mention prefix would push the send over
// it, and Discord rejects the whole message.
func TestChannelNotificationTruncatesIncludingTheMention(t *testing.T) {
	body := strings.Repeat("a", maxChatContent)

	got := channelNotification(body, "123456789012345678")
	if len(got) > maxChatContent {
		t.Errorf("content length = %d, want <= %d (mention prefix pushed it over)", len(got), maxChatContent)
	}
	if !strings.HasPrefix(got, "<@123456789012345678> ") {
		t.Errorf("truncation dropped the owner mention: %q...", got[:min(40, len(got))])
	}
}

func TestDirectMessageNotificationHasNoMentionPrefix(t *testing.T) {
	if got := directMessageNotification("stretch"); got != "stretch" {
		t.Errorf("directMessageNotification = %q, want %q", got, "stretch")
	}
	if got := directMessageNotification(""); got != emptyReminderBody {
		t.Errorf("empty DM body = %q, want %q", got, emptyReminderBody)
	}
	if got := directMessageNotification(strings.Repeat("a", maxChatContent+10)); len(got) > maxChatContent {
		t.Errorf("DM body length = %d, want <= %d", len(got), maxChatContent)
	}
}

// The @everyone guard: a reminder body is user-authored text posted by the bot,
// so it is a ping vector unless Parse stays empty.
func TestMentionOwnerOnlyPermitsExactlyOnePing(t *testing.T) {
	allowed := mentionOwnerOnly("123")
	if allowed == nil {
		t.Fatal("mentionOwnerOnly returned nil, which lets Discord parse every mention")
	}
	if allowed.Parse == nil {
		t.Error("Parse is nil (omitted), which means 'parse everything' to Discord")
	}
	if len(allowed.Parse) != 0 {
		t.Errorf("Parse = %v, want empty so @everyone and roles in the body stay inert", allowed.Parse)
	}
	if len(allowed.Users) != 1 || allowed.Users[0] != "123" {
		t.Errorf("Users = %v, want exactly [123]", allowed.Users)
	}
	if len(allowed.Roles) != 0 {
		t.Errorf("Roles = %v, want empty", allowed.Roles)
	}
}

// The body is carried verbatim, so the whole defence is that Parse is empty and
// the owner is the only entry in AllowedMentions. Both halves asserted together.
func TestAnEveryoneInTheReminderBodyStaysInert(t *testing.T) {
	const ownerUID = "123"
	d := delivery("id-1", "@everyone stand up and stretch", "chan-1", ownerUID)

	body := channelNotification(d.GetMessage(), d.GetOwnerUid())
	if !strings.Contains(body, "@everyone stand up and stretch") {
		t.Errorf("channelNotification = %q, want the reminder body carried verbatim", body)
	}

	allowed := mentionOwnerOnly(d.GetOwnerUid())
	if allowed == nil {
		t.Fatal("mentionOwnerOnly returned nil, so Discord would parse the @everyone in the body")
	}
	if len(allowed.Parse) != 0 {
		t.Errorf("Parse = %v, want empty; a non-empty Parse makes the @everyone in the body live", allowed.Parse)
	}
	if len(allowed.Users) != 1 || allowed.Users[0] != ownerUID {
		t.Errorf("Users = %v, want exactly [%s]", allowed.Users, ownerUID)
	}
	if len(allowed.Roles) != 0 {
		t.Errorf("Roles = %v, want empty", allowed.Roles)
	}
}

func TestMentionOwnerOnlyWithoutAnOwnerAllowsNothing(t *testing.T) {
	allowed := mentionOwnerOnly("")
	if allowed == nil {
		t.Fatal("mentionOwnerOnly(\"\") returned nil, which lets Discord parse every mention")
	}
	if len(allowed.Parse) != 0 || len(allowed.Users) != 0 || len(allowed.Roles) != 0 {
		t.Errorf("allowed = %+v, want nothing permitted", allowed)
	}
}

func TestNoMentionsIsStillTheDMPolicy(t *testing.T) {
	// discordgo treats a nil AllowedMentions as "mention everything".
	//
	//nolint:staticcheck // QF1011 wants the type dropped because it is
	// inferable. It is, and that is the point: this line exists to fail
	// compilation if the inferred type ever changes.
	var _ *discordgo.MessageAllowedMentions = noMentions()

	allowed := noMentions()
	if allowed == nil || allowed.Parse == nil || len(allowed.Parse) != 0 {
		t.Errorf("noMentions() = %+v, want a non-nil empty Parse list", allowed)
	}
}
