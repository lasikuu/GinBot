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

// TestMain gives this package a logger. Every function under test logs, and
// log.Z is nil until something initialises it — a nil deref in a test tells us
// nothing about the code.
//
// discordSession is deliberately left nil: it is the seam these tests rely on.
// A branch that must not touch Discord is proved not to by the fact that
// touching it would panic here, and no fake session is built for the branches
// that do (see the note on postNotification below).
//
// Service clients travel through the context now (client.Clients, attached by
// withClients), not through a package-level global — see grpcClientsWithNoReminder
// below for the equivalent seam under the Connect port: a *client.Clients whose
// Reminder field is a nil interface, so confirmDelivery calling ConfirmDelivery
// on it panics — which is what makes "this action must not be confirmed" an
// assertion rather than a hope.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// ── Action fixtures ──────────────────────────────────────────────────────────

// notificationAction builds a SEND_NOTIFICATION action carrying delivery. A nil
// delivery leaves the oneof arm UNSET, which is the case the builder's
// nil-check makes expressible and which the client has to survive.
func notificationAction(delivery *pb.ReminderDelivery) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum:     pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction:     &action,
		ReminderDelivery: delivery,
	}.Build()
}

// heartbeatAction builds a SEND_TEST action carrying test, nil leaving the arm
// unset.
func heartbeatAction(test *pb.TestAction) *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &action,
		Test:         test,
	}.Build()
}

// notificationCarryingTheTestArm is a SEND_NOTIFICATION whose payload is the
// wrong arm. Nothing in the schema ties an action to an arm, so this is an
// ordinary input rather than a corrupt one.
func notificationCarryingTheTestArm() *pb.OpenClientActionStreamResp {
	action := pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION

	return pb.OpenClientActionStreamResp_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		ClientAction: &action,
		Test:         pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build(),
	}.Build()
}

// The oneof arm discriminators, as field numbers.
//
// WhichPayload's return type is generated UNEXPORTED (case_…_Payload), so it
// cannot be named in a table in another package. Its underlying type is the
// field number, which is the wire contract the arms are pinned to anyway, so the
// conversion loses nothing.
var (
	payloadNotSet           = protoreflect.FieldNumber(pb.OpenClientActionStreamResp_Payload_not_set_case)
	payloadReminderDelivery = protoreflect.FieldNumber(pb.OpenClientActionStreamResp_ReminderDelivery_case)
	payloadTest             = protoreflect.FieldNumber(pb.OpenClientActionStreamResp_Test_case)
)

// payloadCase reports which arm is set on an action.
func payloadCase(in *pb.OpenClientActionStreamResp) protoreflect.FieldNumber {
	return protoreflect.FieldNumber(in.WhichPayload())
}

// delivery is the ReminderDelivery the cron producer sends, spelled out field by
// field so a test reads as the row it came from.
func delivery(reminderID, message, destinationUID, ownerUID string) *pb.ReminderDelivery {
	return pb.ReminderDelivery_builder{
		ReminderId:     &reminderID,
		Message:        &message,
		DestinationUid: &destinationUID,
		OwnerUid:       &ownerUID,
	}.Build()
}

// ── The payload arm ──────────────────────────────────────────────────────────

// TestReminderDeliveryAcceptsOnlyItsOwnArm.
//
// The oneof can be unset, and it can carry an arm this handler is not for.
// Neither is a defect on either end — the action and the arm are set by the same
// producer but the schema cannot tie them together — so both have to come back
// as "no delivery" rather than as a nil deref.
//
// WhichPayload is asserted alongside the result because that is what
// reminderDelivery branches on: a case whose discriminator is not what the test
// thinks it is would make the rest of the assertion vacuous.
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

// TestThePayloadArmsAreExactlyTheOnesTheSwitchHandles is the tripwire for a
// future arm.
//
// reminderDelivery switches on WhichPayload with a default that returns nil, so
// an arm added to the schema without a matching branch is SAFE but silent: a
// payload this client was meant to act on would be dropped as "no delivery" and
// nothing would say why. Enumerating the oneof from the descriptor means adding
// an arm fails here, at the switch that has to learn about it, rather than in
// production.
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

// ── handleSendNotification ───────────────────────────────────────────────────

// grpcClientsWithNoReminder builds the context seam
// TestHandleSendNotificationDropsUnusableActionsWithoutPostingOrConfirming
// relies on: a *client.Clients whose Reminder field is left at its zero
// value, a nil ginbotv1connect.ReminderServiceClient interface. Calling a
// method on that nil interface panics exactly the way calling one on the
// old package-level client.ReminderServiceClient global did when it was left
// unassigned, so it still makes "this action must not be confirmed" an
// assertion the test fails on rather than a hope.
func grpcClientsWithNoReminder(ctx context.Context) context.Context {
	return withClients(ctx, &client.Clients{})
}

// TestHandleSendNotificationDropsUnusableActionsWithoutPostingOrConfirming.
//
// Three things are asserted at once, and all three are enforced by what is NOT
// wired up in this package's tests rather than by an expectation object:
//
//   - It must not panic. The test installs no recover(), so one fails the test.
//   - It must not post. discordSession is nil, so any send nil-derefs.
//   - It must not confirm. The context carries a *client.Clients with a nil
//     Reminder interface (grpcClientsWithNoReminder), so ConfirmDelivery on it
//     panics.
//
// The last one is the reason the empty-id case matters and is not merely
// defensive: without an id there is nothing to confirm, so posting the reminder
// would notify the user and then re-notify on every reclaim until the server's
// attempt limit gives up on it. Dropping it unconfirmed lets that limit do its
// job. That rule predates the typed payload and has to survive it.
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
			// The genuinely new failure mode the oneof introduces: an unset arm
			// is representable and a client cannot assume it away.
			name: "no payload arm set",
			in:   notificationAction(nil),
		},
		{
			// An arm this handler is not for. Reading it as a delivery would be
			// the type confusion the oneof exists to make impossible.
			name: "the heartbeat arm instead of a delivery",
			in:   notificationCarryingTheTestArm(),
		},
		{
			name: "a delivery with no fields set at all",
			in:   notificationAction(pb.ReminderDelivery_builder{}.Build()),
		},
		{
			// Everything a post would need, except the one field that makes the
			// post safe to make.
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

// ── handleSendTest ───────────────────────────────────────────────────────────

// TestHandleSendTestSurvivesEveryPayloadArm.
//
// The heartbeat handler reads through two levels of optional message — the arm,
// then emitted_at inside it — and runs inline on the receive loop. A nil deref
// on either costs a delivery and a stack trace in the log for an action that
// carries no information anyone acts on.
func TestHandleSendTestSurvivesEveryPayloadArm(t *testing.T) {
	tests := []struct {
		name string
		in   *pb.OpenClientActionStreamResp
	}{
		{
			// A server built before TestAction existed sends the action and no
			// arm at all.
			name: "no payload arm set",
			in:   heartbeatAction(nil),
		},
		{
			// The arm is set but its only field is not. GetEmittedAt returns nil
			// and AsTime is called on it.
			name: "the heartbeat arm with no emission time",
			in:   heartbeatAction(pb.TestAction_builder{}.Build()),
		},
		{
			name: "a fully populated heartbeat",
			in:   heartbeatAction(pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build()),
		},
		{
			// The wrong arm. Nothing ties the action to the arm, so this is an
			// input rather than a bug.
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

// ── deliveryPlan ─────────────────────────────────────────────────────────────

// TestDeliveryPlanOrdersChannelBeforeDirectMessage covers the whole
// channel-vs-DM-vs-give-up decision as data.
//
// The DM is both the fallback for a channel post that failed and the only route
// for a reminder with no channel; an empty plan is the give-up case, which is
// reported as a failed delivery rather than retried forever.
//
// The routing itself is unchanged by the typed payload, so the cases are too.
// What changed is where the two strings come from: they are read off a real
// ReminderDelivery through the accessors postNotification uses, so a field
// renamed on one side of the wire cannot leave this green.
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
			// destination_uid is legitimately empty: the reminder has no
			// resolvable channel.
			name:      "no channel: DM is the only route",
			delivery:  delivery("id-1", "stretch", "", "owner-1"),
			wantRoute: []deliveryRoute{{Kind: routeDirectMessage, Target: "owner-1"}},
		},
		{
			// owner_uid is legitimately empty: the owner never linked this
			// platform, which costs the DM fallback and nothing else.
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

// TestPostNotificationReportsAFailedDeliveryWhenThereIsNoRoute.
//
// A reminder with neither a channel nor an owner cannot be delivered at all.
// That has to be reported as delivered=false — which the caller confirms, so the
// server marks the attempt spent — rather than left unconfirmed and re-pushed on
// every reclaim until the attempt limit runs out.
//
// This is the one postNotification path reachable without a Discord session, and
// it is reachable precisely because an empty plan returns before any send. A
// regression that dropped the len(plan) == 0 guard would nil-deref
// discordSession here instead of quietly returning true.
func TestPostNotificationReportsAFailedDeliveryWhenThereIsNoRoute(t *testing.T) {
	if discordSession != nil {
		t.Fatal("discordSession must stay nil for this test to mean anything")
	}

	if postNotification(delivery("id-1", "stretch", "", "")) {
		t.Error("postNotification reported a delivery for a reminder with no channel and no owner")
	}
}

// ── message rendering and mention policy ─────────────────────────────────────

// TestChannelNotificationMentionsTheOwner: in a shared channel the reminder has
// to ping the person who asked for it, otherwise nobody is notified and nothing
// says whose reminder it is.
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

// TestChannelNotificationTruncatesIncludingTheMention: truncation has to be
// applied to the final content, or a body just under the limit plus a mention
// prefix would push the send over it and Discord would reject the whole message.
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

// TestDirectMessageNotificationHasNoMentionPrefix: a DM already goes to exactly
// one person, so mentioning them inside it is noise.
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

// TestMentionOwnerOnlyPermitsExactlyOnePing is the @everyone guard.
//
// A reminder body is user-authored text posted by the bot, so it is an
// @everyone/role-ping vector unless Parse stays empty. Allowing the owner under
// Users must not re-enable parsing of anything else.
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

// TestAnEveryoneInTheReminderBodyStaysInert drives the same guard from the wire
// message, because the retyping is where it could have been lost.
//
// The body is carried verbatim — stripping it would be the wrong fix, since it
// mangles a reminder that legitimately says "@everyone" — so the whole defence
// is that the owner is the only entry in AllowedMentions and Parse is empty.
// Both halves are asserted together, from one ReminderDelivery, because either
// one alone is satisfiable while the other is broken.
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

// TestMentionOwnerOnlyWithoutAnOwnerAllowsNothing: an unknown owner must not
// widen the policy — it falls back to suppressing every mention.
func TestMentionOwnerOnlyWithoutAnOwnerAllowsNothing(t *testing.T) {
	allowed := mentionOwnerOnly("")
	if allowed == nil {
		t.Fatal("mentionOwnerOnly(\"\") returned nil, which lets Discord parse every mention")
	}
	if len(allowed.Parse) != 0 || len(allowed.Users) != 0 || len(allowed.Roles) != 0 {
		t.Errorf("allowed = %+v, want nothing permitted", allowed)
	}
}

// TestNoMentionsIsStillTheDMPolicy pins the type the DM path uses, so a change
// to noMentions cannot silently loosen reminder DMs.
func TestNoMentionsIsStillTheDMPolicy(t *testing.T) {
	// The exact return type is the assertion, not incidental to it: discordgo
	// treats a nil AllowedMentions as "use the default", which mentions
	// everything the content names. Reading .Parse below would still compile
	// against some other struct that happens to have the field.
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

// NOTE on coverage left to inspection, deliberately.
//
// sendChannelMessage, sendDirectMessage and confirmDelivery all dereference a
// package-level *discordgo.Session or a package-level gRPC client stub, as does
// postNotification once its plan is non-empty. Reaching them from a test needs
// either a fake session — discordgo exposes no interface to fake, only a
// concrete struct whose HTTP client would have to be intercepted — or a mocking
// library. Neither is worth it here: every decision those functions make has
// been extracted above (reminderDelivery, deliveryPlan, channelNotification,
// directMessageNotification, mentionOwnerOnly) and is table-tested, leaving them
// as thin wiring over an HTTP call.
//
// The one behaviour that genuinely cannot be reached without a session is the
// AC11 blocked-user path: UserChannelCreate failing for a user who blocked the
// bot. sendDirectMessage returns false on that error and the caller reports
// delivered=false; that is left to inspection.
