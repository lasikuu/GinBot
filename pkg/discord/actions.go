package discord

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// Action handlers run inline on the receive loop, so an unbounded confirm
// would wedge every later delivery to this client.
const confirmDeliveryTimeout = 10 * time.Second

// emptyReminderBody keeps a text-less reminder from being rejected as empty.
const emptyReminderBody = "\u23f0 Reminder"

func actionHandlers() client.ActionHandlers {
	return client.ActionHandlers{
		pb.ClientAction_CLIENT_ACTION_SEND_TEST:                handleSendTest,
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION:        handleSendNotification,
		pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION_URGENT: handleSendNotification,
	}
}

// handleSendTest logs a development-only heartbeat pushed by cron.
func handleSendTest(_ context.Context, in *pb.OpenClientActionStreamResp) {
	if !in.HasTest() {
		log.Z.Debug("received test action with no test payload")
		return
	}

	log.Z.Debug("received test action",
		zap.Time("emitted_at", in.GetTest().GetEmittedAt().AsTime()))
}

// The schema cannot tie the action to its payload arm, so a mismatched or
// absent arm is ordinary input rather than an invariant this client may assume.
func reminderDelivery(in *pb.OpenClientActionStreamResp) *pb.ReminderDelivery {
	switch in.WhichPayload() {
	case pb.OpenClientActionStreamResp_ReminderDelivery_case:
		return in.GetReminderDelivery()
	case pb.OpenClientActionStreamResp_Test_case,
		pb.OpenClientActionStreamResp_Payload_not_set_case:
		return nil
	default:
		return nil
	}
}

// routeKind is how one delivery attempt reaches the user.
type routeKind int

const (
	routeChannel routeKind = iota
	routeDirectMessage
)

// deliveryRoute is one attempt: where to post, and to whom.
type deliveryRoute struct {
	Kind   routeKind
	Target string
}

// deliveryPlan orders the routes to try. An empty plan means the delivery
// cannot be attempted at all, and is reported failed rather than retried.
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

// Truncation covers the mention prefix too, so it cannot push the message over
// Discord's length limit.
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

func directMessageNotification(message string) string {
	if message == "" {
		return emptyReminderBody
	}

	return truncateContent(message)
}

// mentionOwnerOnly permits exactly one ping. Parse stays empty on purpose: the
// body is user-authored, so an @everyone or role ping inside it must stay inert.
func mentionOwnerOnly(ownerUID string) *discordgo.MessageAllowedMentions {
	if ownerUID == "" {
		return noMentions()
	}

	return &discordgo.MessageAllowedMentions{
		Parse: []discordgo.AllowedMentionType{},
		Users: []string{ownerUID},
	}
}

// handleSendNotification posts a reminder and confirms the outcome. Delivery is
// best-effort with a DM fallback; a total failure is reported, never retried.
func handleSendNotification(ctx context.Context, in *pb.OpenClientActionStreamResp) {
	// Without an id the delivery cannot be confirmed, so posting it would
	// notify the user and then repeat on every reclaim.
	delivery := reminderDelivery(in)
	if delivery.GetReminderId() == "" {
		log.Z.Warn("notification action carried no reminder id; dropping it",
			zap.String("payload", in.WhichPayload().String()))
		return
	}

	delivered := postNotification(delivery)

	confirmDelivery(ctx, delivery.GetReminderId(), delivery.GetOwnerUid(), delivered)
}

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

// A user who has blocked the bot fails UserChannelCreate or the send; both are
// non-fatal failed deliveries.
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

// The server scopes ConfirmDelivery to the reminder's owner, so the owner's
// platform identity is attached to the outgoing context as a request header.
func confirmDelivery(ctx context.Context, reminderID string, ownerUID string, delivered bool) {
	if ownerUID == "" {
		log.Z.Warn("confirming reminder delivery without an owner id; server will reject",
			zap.String("reminder_id", reminderID))
	}

	callCtx, cancel := context.WithTimeout(ctx, confirmDeliveryTimeout)
	defer cancel()

	outCtx := callermeta.NewOutgoingContext(callCtx, pb.Platform_PLATFORM_DISCORD, ownerUID)

	req := pb.ConfirmDeliveryReq_builder{
		Id:        &reminderID,
		Delivered: &delivered,
	}.Build()

	clients := clientsFrom(outCtx)
	if _, err := clients.Reminder.ConfirmDelivery(outCtx, connect.NewRequest(req)); err != nil {
		log.Z.Error("failed to confirm reminder delivery",
			zap.String("reminder_id", reminderID),
			zap.Bool("delivered", delivered),
			zap.Error(err))
	}
}
