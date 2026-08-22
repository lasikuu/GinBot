package discord

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/reminder"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestMain gives this package a logger. Every function under test logs, and
// log.Z is nil until something initialises it — a nil deref in a test tells us
// nothing about the code.
//
// discordSession is deliberately left nil: it is the seam these tests rely on.
// A branch that must not touch Discord is proved not to by the fact that
// touching it would panic here, and no fake session is built for the branches
// that do (see the note on postNotification below).
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// ── structString ─────────────────────────────────────────────────────────────

// TestStructStringHandlesEveryMissingShape: the delivery payload is untyped, so
// every read has to survive a missing key, an explicit nil Value, and a Value
// that is not a string. This runs inline on the receive loop with no recover(),
// so a nil deref here kills the process.
func TestStructStringHandlesEveryMissingShape(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]*structpb.Value
		key    string
		want   string
	}{
		{
			name:   "nil map",
			fields: nil,
			key:    "k",
			want:   "",
		},
		{
			name:   "missing key",
			fields: map[string]*structpb.Value{"other": structpb.NewStringValue("v")},
			key:    "k",
			want:   "",
		},
		{
			name:   "nil value",
			fields: map[string]*structpb.Value{"k": nil},
			key:    "k",
			want:   "",
		},
		{
			name:   "wrong type",
			fields: map[string]*structpb.Value{"k": structpb.NewNumberValue(42)},
			key:    "k",
			want:   "",
		},
		{
			name:   "empty string",
			fields: map[string]*structpb.Value{"k": structpb.NewStringValue("")},
			key:    "k",
			want:   "",
		},
		{
			name:   "present",
			fields: map[string]*structpb.Value{"k": structpb.NewStringValue("v")},
			key:    "k",
			want:   "v",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := structString(tt.fields, tt.key); got != tt.want {
				t.Errorf("structString = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── parseNotification ────────────────────────────────────────────────────────

// TestParseNotificationReadsTheProductionPayload: a payload built by the real
// builder must come back out field for field. This is the other half of the
// contract asserted in pkg/reminder — writer there, reader here.
func TestParseNotificationReadsTheProductionPayload(t *testing.T) {
	const (
		reminderID = "0192f000-0000-7000-8000-000000000001"
		message    = "stand up and stretch"
		channelID  = "1234567890"
		ownerUID   = "9876543210"
	)

	n, ok := parseNotification(reminder.NewDeliveryPayload(reminderID, message, channelID, ownerUID))
	if !ok {
		t.Fatal("parseNotification rejected a well-formed production payload")
	}
	if n.ReminderID != reminderID {
		t.Errorf("ReminderID = %q, want %q", n.ReminderID, reminderID)
	}
	if n.Message != message {
		t.Errorf("Message = %q, want %q", n.Message, message)
	}
	if n.ChannelID != channelID {
		t.Errorf("ChannelID = %q, want %q", n.ChannelID, channelID)
	}
	if n.OwnerUID != ownerUID {
		t.Errorf("OwnerUID = %q, want %q", n.OwnerUID, ownerUID)
	}
}

// TestParseNotificationRejectsPayloadsWithoutAReminderID: without an id the
// delivery cannot be confirmed, so posting it would notify the user and then
// re-notify on every reclaim until the attempt cap. Refusing is the only safe
// answer. Everything else is optional.
func TestParseNotificationRejectsPayloadsWithoutAReminderID(t *testing.T) {
	const reminderID = "0192f000-0000-7000-8000-000000000002"

	tests := []struct {
		name    string
		content *structpb.Struct
		wantOK  bool
	}{
		{
			name:    "nil struct",
			content: nil,
			wantOK:  false,
		},
		{
			name:    "no fields at all",
			content: &structpb.Struct{},
			wantOK:  false,
		},
		{
			name: "reminder id missing",
			content: &structpb.Struct{Fields: map[string]*structpb.Value{
				reminder.PayloadKeyMessage: structpb.NewStringValue("orphan"),
			}},
			wantOK: false,
		},
		{
			name:    "reminder id empty",
			content: reminder.NewDeliveryPayload("", "orphan", "chan", "owner"),
			wantOK:  false,
		},
		{
			name: "reminder id nil value",
			content: &structpb.Struct{Fields: map[string]*structpb.Value{
				reminder.PayloadKeyReminderID: nil,
			}},
			wantOK: false,
		},
		{
			name:    "reminder id only",
			content: reminder.NewDeliveryPayload(reminderID, "", "", ""),
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := parseNotification(tt.content)
			if ok != tt.wantOK {
				t.Fatalf("parseNotification ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok && n.ReminderID != "" {
				t.Errorf("rejected notification still carries id %q", n.ReminderID)
			}
		})
	}
}

// TestHandleSendNotificationDropsUnusablePayloadsWithoutTouchingDiscord.
//
// discordSession is nil in this package's tests, so if either of these payloads
// reached a send the test would panic rather than fail. That is exactly the
// assertion: an unusable payload must be dropped before any Discord or gRPC call
// — both of which would nil-deref here — and must not panic while doing so.
func TestHandleSendNotificationDropsUnusablePayloadsWithoutTouchingDiscord(t *testing.T) {
	if discordSession != nil {
		t.Fatal("discordSession must stay nil for this test to mean anything")
	}

	action := pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION
	tests := []struct {
		name    string
		content *structpb.Struct
	}{
		{name: "no content", content: nil},
		{name: "no reminder id", content: reminder.NewDeliveryPayload("", "orphan", "chan", "owner")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := pb.OpenClientActionStreamResp_builder{
				PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
				ClientAction: &action,
				Content:      tt.content,
			}.Build()

			// A panic fails the test by itself; no recover() is installed.
			handleSendNotification(context.Background(), in)
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
func TestDeliveryPlanOrdersChannelBeforeDirectMessage(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		ownerUID  string
		want      []deliveryRoute
	}{
		{
			name:      "channel and owner: channel first, DM as fallback",
			channelID: "chan-1",
			ownerUID:  "owner-1",
			want: []deliveryRoute{
				{Kind: routeChannel, Target: "chan-1"},
				{Kind: routeDirectMessage, Target: "owner-1"},
			},
		},
		{
			name:      "no channel: DM is the only route",
			channelID: "",
			ownerUID:  "owner-1",
			want:      []deliveryRoute{{Kind: routeDirectMessage, Target: "owner-1"}},
		},
		{
			name:      "no owner: channel only, no fallback",
			channelID: "chan-1",
			ownerUID:  "",
			want:      []deliveryRoute{{Kind: routeChannel, Target: "chan-1"}},
		},
		{
			name:      "neither: give up",
			channelID: "",
			ownerUID:  "",
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deliveryPlan(tt.channelID, tt.ownerUID)
			if len(got) != len(tt.want) {
				t.Fatalf("plan = %+v, want %+v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("plan[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ── message rendering and mention policy ─────────────────────────────────────

// TestChannelNotificationMentionsTheOwner: in a shared channel the reminder has
// to ping the person who asked for it, otherwise nobody is notified and nothing
// says whose reminder it is.
func TestChannelNotificationMentionsTheOwner(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		ownerUID string
		want     string
	}{
		{
			name:     "owner mention prefixes the body",
			message:  "stretch",
			ownerUID: "123",
			want:     "<@123> stretch",
		},
		{
			name:     "empty message still notifies",
			message:  "",
			ownerUID: "123",
			want:     "<@123> " + emptyReminderBody,
		},
		{
			name:     "no owner: body only, no broken mention",
			message:  "stretch",
			ownerUID: "",
			want:     "stretch",
		},
		{
			name:     "no owner and no message",
			message:  "",
			ownerUID: "",
			want:     emptyReminderBody,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := channelNotification(tt.message, tt.ownerUID); got != tt.want {
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
	var _ *discordgo.MessageAllowedMentions = noMentions()

	if allowed := noMentions(); allowed == nil || allowed.Parse == nil || len(allowed.Parse) != 0 {
		t.Errorf("noMentions() = %+v, want a non-nil empty Parse list", allowed)
	}
}

// NOTE on coverage left to inspection, deliberately.
//
// postNotification, sendChannelMessage, sendDirectMessage and confirmDelivery
// all dereference a package-level *discordgo.Session or a package-level gRPC
// client stub. Reaching them from a test needs either a fake session — discordgo
// exposes no interface to fake, only a concrete struct whose HTTP client would
// have to be intercepted — or a mocking library. Neither is worth it here: every
// decision those functions make has been extracted above (parseNotification,
// deliveryPlan, channelNotification, directMessageNotification,
// mentionOwnerOnly) and is table-tested, leaving them as thin wiring over an
// HTTP call.
//
// The one behaviour that genuinely cannot be reached without a session is the
// AC11 blocked-user path: UserChannelCreate failing for a user who blocked the
// bot. sendDirectMessage returns false on that error and the caller reports
// delivered=false; that is left to inspection.
