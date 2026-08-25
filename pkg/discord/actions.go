package discord

import (
	"context"
	"time"

	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/reminder"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// confirmDeliveryTimeout bounds the outgoing ConfirmDelivery call.
//
// pkg/grpc/client.dispatch runs action handlers INLINE on the receive loop, and
// no gRPC keepalive is configured, so a half-open connection would let this call
// block forever — wedging every further action delivery to this client, not just
// this one reminder. postNotification is already bounded by discordgo's HTTP
// timeout; the gRPC call had no bound at all. Ten seconds is far longer than a
// healthy confirm needs and short enough that a wedge unsticks itself.
const confirmDeliveryTimeout = 10 * time.Second

// emptyReminderBody is posted for a reminder with no text, so the send is not
// rejected for empty content. A reminder with no message still has to notify.
const emptyReminderBody = "\u23f0 Reminder"

// actionHandlers maps server-pushed actions to their Discord implementations.
func actionHandlers() client.ActionHandlers {
	return client.ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST:                handleSendTest,
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION:        handleSendNotification,
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION_URGENT: handleSendNotification,
	}
}

// handleSendTest logs a development-only heartbeat pushed by cron.
func handleSendTest(_ context.Context, in *pb.OpenClientActionStreamResp) {
	log.Z.Debug("received test action", zap.Any("content", in.GetContent().AsMap()))
}

// notification is the validated content of a SEND_NOTIFICATION action.
type notification struct {
	ReminderID string
	Message    string
	ChannelID  string
	OwnerUID   string
}

// parseNotification reads a notification out of the untyped action content.
//
// ok is false when the payload carries no reminder id: without one the delivery
// cannot be confirmed, so posting it would notify the user and then loop forever
// on every reclaim. Every other field is optional and defaults to empty.
//
// It must never panic. discordgo dispatches handlers as bare goroutines with no
// recover(), so a nil deref here still takes the whole process down on that
// path — hence a nil Struct and a nil field value are both handled rather than
// assumed away. pkg/grpc/client.dispatch does now recover around the handlers it
// calls inline, but that is belt to this function's braces: it downgrades a
// panic to one lost delivery, it does not make one acceptable.
func parseNotification(content *structpb.Struct) (notification, bool) {
	if content == nil {
		return notification{}, false
	}

	fields := content.GetFields()
	n := notification{
		ReminderID: structString(fields, reminder.PayloadKeyReminderID),
		Message:    structString(fields, reminder.PayloadKeyMessage),
		ChannelID:  structString(fields, reminder.PayloadKeyDestinationUID),
		OwnerUID:   structString(fields, reminder.PayloadKeyUserID),
	}

	return n, n.ReminderID != ""
}

// routeKind is how one delivery attempt reaches the user.
type routeKind int

const (
	// routeChannel posts to the reminder's own channel.
	routeChannel routeKind = iota
	// routeDirectMessage opens a DM to the reminder's owner and posts there.
	routeDirectMessage
)

// deliveryRoute is one attempt: where to post, and to whom.
type deliveryRoute struct {
	Kind   routeKind
	Target string
}

// deliveryPlan is the ordered list of routes to try for one notification.
//
// The channel comes first when the reminder has one. The owner's DM is both the
// fallback for a channel that is gone or that the bot cannot post in, and the
// only route for a reminder with no channel at all. An EMPTY plan means the
// delivery cannot even be attempted (no channel and no owner), which is reported
// as a failed delivery rather than retried forever.
//
// It is a pure function so the routing can be tested without a Discord session.
func deliveryPlan(channelID, ownerUID string) []deliveryRoute {
	var plan []deliveryRoute
	if channelID != "" {
		plan = append(plan, deliveryRoute{Kind: routeChannel, Target: channelID})
	}
	if ownerUID != "" {
		plan = append(plan, deliveryRoute{Kind: routeDirectMessage, Target: ownerUID})
	}

	return plan
}

// channelNotification renders the message posted to a shared channel.
//
// The owner is mentioned so that, in a channel other people can see, the
// reminder pings the person who asked for it and says whose reminder it is.
// Truncation is applied to the whole thing, mention included, so the prefix
// cannot push the message over Discord's limit.
func channelNotification(message, ownerUID string) string {
	body := message
	if body == "" {
		body = emptyReminderBody
	}
	if ownerUID != "" {
		body = "<@" + ownerUID + "> " + body
	}

	return truncateContent(body)
}

// directMessageNotification renders the DM body. No mention prefix: a DM already
// goes to exactly one person, and mentioning them inside it adds nothing.
func directMessageNotification(message string) string {
	if message == "" {
		return emptyReminderBody
	}

	return truncateContent(message)
}

// mentionOwnerOnly permits exactly one ping: the reminder's owner.
//
// Parse stays EMPTY on purpose. The reminder body is user-authored text, so an
// @everyone or a role ping inside it must stay inert; listing the owner under
// Users allows that single mention without re-enabling parsing of anything else.
// An empty owner allows nothing at all, which is noMentions' behaviour.
func mentionOwnerOnly(ownerUID string) *discordgo.MessageAllowedMentions {
	if ownerUID == "" {
		return noMentions()
	}

	return &discordgo.MessageAllowedMentions{
		Parse: []discordgo.AllowedMentionType{},
		Users: []string{ownerUID},
	}
}

// handleSendNotification posts a reminder to its channel and confirms the
// outcome back to the server.
//
// Delivery is best-effort with a DM fallback: if the channel post fails (channel
// gone, or the bot lacks Send Messages there) it opens a direct message to the
// reminder owner instead. A user who has blocked the bot makes both the DM open
// and the send fail; that is treated as a non-fatal failed delivery
// (delivered=false), not a crash and not a wedged receive loop.
func handleSendNotification(ctx context.Context, in *pb.OpenClientActionStreamResp) {
	n, ok := parseNotification(in.GetContent())
	if !ok {
		log.Z.Warn("notification action carried no reminder id; dropping it")
		return
	}

	delivered := postNotification(n)

	confirmDelivery(ctx, n.ReminderID, n.OwnerUID, delivered)
}

// postNotification walks the delivery plan and reports whether the reminder
// reached the user by any route.
func postNotification(n notification) bool {
	plan := deliveryPlan(n.ChannelID, n.OwnerUID)
	if len(plan) == 0 {
		log.Z.Warn("reminder has neither a channel nor an owner to notify",
			zap.String("reminder_id", n.ReminderID))
		return false
	}

	for _, route := range plan {
		if route.Kind == routeChannel {
			if sendChannelMessage(route.Target, channelNotification(n.Message, n.OwnerUID), mentionOwnerOnly(n.OwnerUID)) {
				return true
			}
			continue
		}
		if sendDirectMessage(route.Target, directMessageNotification(n.Message)) {
			return true
		}
	}

	return false
}

// sendChannelMessage posts to a channel, reporting success.
func sendChannelMessage(channelID, body string, allowed *discordgo.MessageAllowedMentions) bool {
	_, err := discordSession.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         body,
		AllowedMentions: allowed,
	})
	if err != nil {
		log.Z.Warn("failed to post reminder to channel, trying the next route",
			zap.String("channel_id", channelID), zap.Error(err))
		return false
	}

	return true
}

// sendDirectMessage opens a DM channel to the user and posts to it. A user who
// has blocked the bot yields an error from UserChannelCreate or the send; both
// are treated as a non-fatal failed delivery.
func sendDirectMessage(userID string, body string) bool {
	channel, err := discordSession.UserChannelCreate(userID)
	if err != nil {
		log.Z.Warn("failed to open DM channel for reminder",
			zap.String("user_id", userID), zap.Error(err))
		return false
	}

	_, err = discordSession.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
		Content:         body,
		AllowedMentions: noMentions(),
	})
	if err != nil {
		log.Z.Warn("failed to send reminder DM",
			zap.String("user_id", userID), zap.Error(err))
		return false
	}

	return true
}

// confirmDelivery reports the outcome to the server. ConfirmDelivery is
// clearance-gated, reads the caller from metadata and is scoped to the
// reminder's owner, so the owner's platform identity is attached to the outgoing
// context — the same identity the reminder was created under.
//
// A missing owner uid still confirms: an empty uid produces a call with no user
// metadata, which the interceptor rejects. That is survivable rather than
// correct — the server's reclaim eventually gives up on the reminder after
// maxDeliveryAttempts instead of re-posting it forever — but it is logged so a
// persistently uid-less reminder is visible.
func confirmDelivery(ctx context.Context, reminderID string, ownerUID string, delivered bool) {
	if ownerUID == "" {
		log.Z.Warn("confirming reminder delivery without an owner id; server will reject",
			zap.String("reminder_id", reminderID))
	}

	// Bounded: this runs inline on the receive loop, so an unbounded call stalls
	// every later action for this client.
	callCtx, cancel := context.WithTimeout(ctx, confirmDeliveryTimeout)
	defer cancel()

	outCtx := callermeta.NewOutgoingContext(callCtx, pb.Platform_PLATFORM_DISCORD, ownerUID)

	req := pb.ConfirmDeliveryReq_builder{
		Id:        &reminderID,
		Delivered: &delivered,
	}.Build()

	if _, err := client.ReminderServiceClient.ConfirmDelivery(outCtx, req); err != nil {
		log.Z.Error("failed to confirm reminder delivery",
			zap.String("reminder_id", reminderID),
			zap.Bool("delivered", delivered),
			zap.Error(err))
	}
}

// structString reads a string field from an untyped payload, returning "" for a
// missing or non-string field. It guards against every nil the opaque Struct
// accessors would otherwise deref.
func structString(fields map[string]*structpb.Value, key string) string {
	value, ok := fields[key]
	if !ok || value == nil {
		return ""
	}

	return value.GetStringValue()
}
