package discord

import (
	"context"
	"time"

	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
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
//
// The emission time is logged rather than the whole message: the action itself
// says nothing an operator does not already know, so the only information in a
// heartbeat is how stale it is by the time it lands here.
func handleSendTest(_ context.Context, in *pb.OpenClientActionStreamResp) {
	if !in.HasTest() {
		// Not a defect on either end: SEND_TEST carries no payload from a
		// server built before TestAction existed, and the heartbeat is still a
		// heartbeat without one.
		log.Z.Debug("received test action with no test payload")
		return
	}

	log.Z.Debug("received test action",
		zap.Time("emitted_at", in.GetTest().GetEmittedAt().AsTime()))
}

// reminderDelivery returns the ReminderDelivery the action carries, or nil when
// it carries something else or nothing at all.
//
// The action and the payload arm are set by the same producer but the schema
// cannot tie them together, so SEND_NOTIFICATION arriving with a different arm
// — or with none — is an input this client has to handle, not an invariant it
// may assume. Returning nil rather than panicking matters because
// pkg/grpc/client.dispatch runs handlers inline on the receive loop: its
// recover() downgrades a panic to one lost delivery, it does not make one
// acceptable.
func reminderDelivery(in *pb.OpenClientActionStreamResp) *pb.ReminderDelivery {
	switch in.WhichPayload() {
	case pb.OpenClientActionStreamResp_ReminderDelivery_case:
		return in.GetReminderDelivery()
	case pb.OpenClientActionStreamResp_Test_case,
		pb.OpenClientActionStreamResp_Payload_not_set_case:
		return nil
	default:
		// An arm added to the schema after this switch was written. An arm this
		// BUILD does not know about does not reach here — protobuf keeps an
		// unrecognised field in unknownFields and WhichPayload reports not-set
		// — so this branch is the guard against a future arm being silently
		// treated as a reminder, and must stay even though it is unreachable
		// against the schema this file is compiled with.
		return nil
	}
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
	// GetReminderId is nil-safe, so an absent payload and a payload with an
	// empty id fail the same check. Both are refused for the same reason:
	// without an id the delivery cannot be confirmed, so posting it would
	// notify the user and then loop forever on every reclaim. Dropping it
	// unconfirmed lets the server's attempt limit fail the reminder out
	// instead.
	delivery := reminderDelivery(in)
	if delivery.GetReminderId() == "" {
		log.Z.Warn("notification action carried no reminder id; dropping it",
			zap.String("payload", in.WhichPayload().String()))
		return
	}

	delivered := postNotification(delivery)

	confirmDelivery(ctx, delivery.GetReminderId(), delivery.GetOwnerUid(), delivered)
}

// postNotification walks the delivery plan and reports whether the reminder
// reached the user by any route.
func postNotification(d *pb.ReminderDelivery) bool {
	ownerUID := d.GetOwnerUid()
	message := d.GetMessage()

	plan := deliveryPlan(d.GetDestinationUid(), ownerUID)
	if len(plan) == 0 {
		log.Z.Warn("reminder has neither a channel nor an owner to notify",
			zap.String("reminder_id", d.GetReminderId()))
		return false
	}

	for _, route := range plan {
		if route.Kind == routeChannel {
			if sendChannelMessage(route.Target, channelNotification(message, ownerUID), mentionOwnerOnly(ownerUID)) {
				return true
			}
			continue
		}
		if sendDirectMessage(route.Target, directMessageNotification(message)) {
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
