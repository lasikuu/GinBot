package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/reminder"
	"go.uber.org/zap"
)

const maxActiveRemindersPerUser = 100

type ReminderServer struct {
	ginbotv1connect.UnimplementedReminderServiceHandler
}

func NewReminderServer() *ReminderServer {
	s := &ReminderServer{}
	return s
}

func (s *ReminderServer) GetReminder(ctx context.Context, connReq *connect.Request[pb.GetReminderReq]) (*connect.Response[pb.GetReminderResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	reminderRow, err := db.GetReminder(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reminder not found"))
	}
	if err != nil {
		log.Z.Error("failed to get reminder", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get reminder"))
	}

	// NotFound rather than PermissionDenied, so it cannot confirm the id exists.
	if reminderRow.UserID == nil || *reminderRow.UserID != caller.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reminder not found"))
	}

	destination, err := db.GetReminderDestination(ctx, reminderRow.DestinationID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		log.Z.Error("failed to resolve reminder destination", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve reminder destination"))
	}

	return connect.NewResponse(pb.GetReminderResp_builder{
		Reminder: reminderRow.ToProto(destination),
	}.Build()), nil
}

// ListReminders is caller-scoped; no request field can widen it to another user.
func (s *ReminderServer) ListReminders(ctx context.Context, connReq *connect.Request[pb.ListRemindersReq]) (*connect.Response[pb.ListRemindersResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	filter := db.ListRemindersFilter{
		UserID: caller.ID,
		Limit:  req.GetLimit(),
		Offset: req.GetOffset(),
	}
	if req.HasMessage() {
		filter.MessageSearch = req.GetMessage()
	}
	if req.HasStatus() {
		statusValue := int32(req.GetStatus().Number())
		filter.Status = &statusValue
	}
	if req.HasPeriodStart() {
		start := req.GetPeriodStart().AsTime()
		filter.PeriodStart = &start
	}
	if req.HasPeriodEnd() {
		end := req.GetPeriodEnd().AsTime()
		filter.PeriodEnd = &end
	}

	reminders, err := db.ListRemindersByUser(ctx, filter)
	if err != nil {
		log.Z.Error("failed to list reminders", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list reminders"))
	}

	out := make([]*pb.Reminder, 0, len(reminders))
	for _, listed := range reminders {
		out = append(out, listed.Reminder.ToProto(listed.Destination))
	}

	return connect.NewResponse(pb.ListRemindersResp_builder{
		Reminders: out,
	}.Build()), nil
}

func (s *ReminderServer) CreateReminder(ctx context.Context, connReq *connect.Request[pb.CreateReminderReq]) (*connect.Response[pb.CreateReminderResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasDatetime() || !req.HasTimezone() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("datetime and timezone are required"))
	}

	if !req.HasDestination() ||
		!req.GetDestination().HasPlatformEnum() ||
		!req.GetDestination().HasInstanceMeta() ||
		!req.GetDestination().HasDestinationMeta() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("destination is required"))
	}

	// The proto regex only checks shape, so "99 99 99 99 99" reaches here.
	if req.HasRepeatCron() {
		if err := reminder.ValidateRepeatInterval(req.GetRepeatCron(), isDMDestination(req.GetDestination())); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", err.Error()))
		}
	}

	destinationID, err := db.GetOrCreateDestinationByMeta(ctx, req.GetDestination())
	if err != nil {
		log.Z.Error("failed to get or create destination", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve destination"))
	}

	// The cap is enforced inside the insert's transaction; check-then-insert would race.
	reminderID, err := db.CreateReminder(ctx, req, caller.ID, destinationID, maxActiveRemindersPerUser)
	if errors.Is(err, db.ErrReminderCapReached) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("you already have the maximum of %d active reminders", maxActiveRemindersPerUser))
	}
	if err != nil {
		log.Z.Error("failed to create reminder", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create reminder"))
	}

	if err := db.CreateActionRecord(ctx, pb.ActionType_ACTION_TYPE_REMINDER_CREATED, caller.ID, nil); err != nil {
		log.Z.Error("failed to record reminder creation", zap.Error(err))
	}

	return connect.NewResponse(pb.CreateReminderResp_builder{
		Id: &reminderID,
	}.Build()), nil
}

func (s *ReminderServer) DeleteReminder(ctx context.Context, connReq *connect.Request[pb.DeleteReminderReq]) (*connect.Response[pb.DeleteReminderResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	// ErrNotFound covers another user's reminder, so it cannot be probed.
	err = db.SoftDeleteReminderByUser(ctx, req.GetId(), caller.ID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reminder not found"))
	}
	if err != nil {
		log.Z.Error("failed to delete reminder", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete reminder"))
	}

	return connect.NewResponse(pb.DeleteReminderResp_builder{}.Build()), nil
}

func (s *ReminderServer) UpdateReminder(ctx context.Context, connReq *connect.Request[pb.UpdateReminderReq]) (*connect.Response[pb.UpdateReminderResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	if !req.HasDatetime() || !req.HasTimezone() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("datetime and timezone are required"))
	}
	if !req.HasDestination() ||
		!req.GetDestination().HasPlatformEnum() ||
		!req.GetDestination().HasInstanceMeta() ||
		!req.GetDestination().HasDestinationMeta() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("destination is required"))
	}

	if !req.GetDatetime().AsTime().After(time.Now()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("datetime must be in the future"))
	}

	// repeat_cron is patch-shaped: absent keeps the schedule, empty clears it.
	updateRepeat := req.HasRepeatCron()
	repeatCron := req.GetRepeatCron()
	if updateRepeat && repeatCron != "" {
		if err := reminder.ValidateRepeatInterval(repeatCron, isDMDestination(req.GetDestination())); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", err.Error()))
		}
	}

	destinationID, err := db.GetOrCreateDestinationByMeta(ctx, req.GetDestination())
	if err != nil {
		log.Z.Error("failed to get or create destination", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve destination"))
	}

	err = db.UpdateReminderByUser(ctx, db.ReminderUpdate{
		ID:            req.GetId(),
		UserID:        caller.ID,
		Datetime:      req.GetDatetime().AsTime(),
		Timezone:      req.GetTimezone(),
		DestinationID: destinationID,
		// message is patch-shaped like repeat_cron: absent keeps the stored
		// text, empty clears it. Writing it unconditionally would NULL the text
		// on a datetime-only update.
		UpdateMessage: req.HasMessage(),
		Message:       req.GetMessage(),
		UpdateRepeat:  updateRepeat,
		RepeatCron:    repeatCron,
	})
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reminder not found"))
	}
	if err != nil {
		log.Z.Error("failed to update reminder", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update reminder"))
	}

	return connect.NewResponse(pb.UpdateReminderResp_builder{}.Build()), nil
}

// ConfirmDelivery is scoped to the reminder's owner. Only a reminder in SENT
// advances, so a retried confirmation is a no-op.
func (s *ReminderServer) ConfirmDelivery(ctx context.Context, connReq *connect.Request[pb.ConfirmDeliveryReq]) (*connect.Response[pb.ConfirmDeliveryResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	reminderRow, err := db.GetReminder(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reminder not found"))
	}
	if err != nil {
		log.Z.Error("failed to load reminder for confirmation", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to confirm delivery"))
	}

	// Without this, any registered caller holding an id could suppress a pending
	// delivery, move a repeat's fire time, or forge an analytics row.
	if reminderRow.UserID == nil || *reminderRow.UserID != caller.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reminder not found"))
	}

	if !req.GetDelivered() {
		failed, err := db.AdvanceReminderStatusIfSent(ctx, req.GetId(), pb.ReminderStatus_REMINDER_STATUS_FAILED)
		if err != nil {
			log.Z.Error("failed to mark reminder failed", zap.Error(err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to confirm delivery"))
		}
		if !failed {
			logConfirmNoOp(req.GetId())
		}
		return connect.NewResponse(pb.ConfirmDeliveryResp_builder{}.Build()), nil
	}

	// Distinguishes a real transition from the guarded no-op, which analytics needs.
	var advanced bool
	if reminderRow.RepeatCron != nil && *reminderRow.RepeatCron != "" {
		loc := reminderLocation(reminderRow.Timezone)
		next, nextErr := reminder.NextOccurrence(*reminderRow.RepeatCron, time.Now(), loc)
		if nextErr != nil {
			// Close the reminder out rather than leave it stuck at SENT.
			log.Z.Warn("repeating reminder has no next occurrence, marking delivered",
				zap.String("reminder_id", req.GetId()), zap.Error(nextErr))
			advanced, err = db.AdvanceReminderStatusIfSent(ctx, req.GetId(), pb.ReminderStatus_REMINDER_STATUS_DELIVERED)
			if err != nil {
				log.Z.Error("failed to mark reminder delivered", zap.Error(err))
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to confirm delivery"))
			}
		} else {
			advanced, err = db.RescheduleReminderIfSent(ctx, req.GetId(), next)
			if err != nil {
				log.Z.Error("failed to reschedule repeating reminder", zap.Error(err))
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to confirm delivery"))
			}
		}
	} else {
		advanced, err = db.AdvanceReminderStatusIfSent(ctx, req.GetId(), pb.ReminderStatus_REMINDER_STATUS_DELIVERED)
		if err != nil {
			log.Z.Error("failed to mark reminder delivered", zap.Error(err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to confirm delivery"))
		}
	}

	// A duplicate confirmation must not produce a second analytics row.
	if !advanced {
		logConfirmNoOp(req.GetId())
		return connect.NewResponse(pb.ConfirmDeliveryResp_builder{}.Build()), nil
	}

	// Best-effort, and recorded against the owner rather than the caller.
	if err := db.CreateActionRecord(ctx, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED, *reminderRow.UserID, nil); err != nil {
		log.Z.Error("failed to record reminder delivery", zap.Error(err))
	}

	return connect.NewResponse(pb.ConfirmDeliveryResp_builder{}.Build()), nil
}

func logConfirmNoOp(reminderID string) {
	log.Z.Debug("confirmation did not advance a reminder; it was not in SENT",
		zap.String("reminder_id", reminderID))
}

// isDMDestination reports a destination carrying no instance_uid.
func isDMDestination(destination *pb.ReminderDestination) bool {
	instanceMeta := destination.GetInstanceMeta()
	if instanceMeta == nil {
		return true
	}
	value, ok := instanceMeta.GetFields()[callermeta.FieldInstanceUID]
	if !ok {
		return true
	}

	return value.GetStringValue() == ""
}

// reminderLocation defaults to UTC on an empty or unknown zone.
func reminderLocation(timezone string) *time.Location {
	if timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Z.Warn("reminder has an unknown timezone, using UTC",
			zap.String("timezone", timezone), zap.Error(err))
		return time.UTC
	}

	return loc
}
