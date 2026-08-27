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

// maxActiveRemindersPerUser caps how many pending reminders one user may hold.
//
// The limit is FailedPrecondition rather than ResourceExhausted: the caller can
// fix it themselves (delete a reminder) and the errorMessage mapping surfaces
// FailedPrecondition verbatim, so the user is told the actual limit instead of a
// generic "resource exhausted".
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

	// Reminders are private to their creator. NotFound rather than
	// PermissionDenied so the response does not confirm that the id exists.
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

// ListReminders is caller-scoped: only the caller's own reminders are returned.
// The optional filters (limit, offset, status, period, message search) narrow
// within that scope, and the request has no field that could widen it to another
// user.
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

	// The destinations arrive with the rows: ListRemindersByUser joins them, so a
	// listing is one query rather than one plus a destination lookup per row.
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

	// The validation interceptor covers these too, but it is wired only in
	// cmd/ginbot-server, and AGENTS.md requires handlers to check required
	// fields themselves.
	if !req.HasDatetime() || !req.HasTimezone() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("datetime and timezone are required"))
	}

	if !req.HasDestination() ||
		!req.GetDestination().HasPlatformEnum() ||
		!req.GetDestination().HasInstanceMeta() ||
		!req.GetDestination().HasDestinationMeta() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("destination is required"))
	}

	// Validate the repeat schedule semantically. The proto's regex only checks
	// the string's shape, so "99 99 99 99 99" reaches here; reject it, and reject
	// a repeat more frequent than the floor for its destination.
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

	// The per-user cap is enforced inside the insert's transaction, not by a
	// count here: a check-then-insert lets two concurrent creates at the limit
	// both pass the check and both write.
	reminderID, err := db.CreateReminder(ctx, req, caller.ID, destinationID, maxActiveRemindersPerUser)
	if errors.Is(err, db.ErrReminderCapReached) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("you already have the maximum of %d active reminders", maxActiveRemindersPerUser))
	}
	if err != nil {
		log.Z.Error("failed to create reminder", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create reminder"))
	}

	// Analytics: record the creation. A failure here must not fail the create —
	// the reminder exists and will fire — so it is logged, not returned.
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

	// Caller-scoped soft delete. ErrNotFound covers another user's reminder, so
	// a caller cannot delete or probe one that is not theirs.
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

	// A reminder can only be moved into the future.
	if !req.GetDatetime().AsTime().After(time.Now()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("datetime must be in the future"))
	}

	// repeat_cron is the one patch-shaped field on this request: absent means
	// "leave the stored schedule alone", so an edit that only changes the message
	// no longer destroys an existing repeat. An explicitly EMPTY value is the
	// documented sentinel for clearing it.
	//
	// A supplied schedule gets exactly the checks create applies — the same
	// semantic cron parse and the same minimum-interval floor for the
	// destination — because an update could otherwise install a schedule create
	// would have refused.
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

	// Caller-scoped update; ErrNotFound covers another user's reminder, matching
	// GetReminder's privacy pattern.
	err = db.UpdateReminderByUser(ctx, db.ReminderUpdate{
		ID:            req.GetId(),
		UserID:        caller.ID,
		Datetime:      req.GetDatetime().AsTime(),
		Timezone:      req.GetTimezone(),
		Message:       req.GetMessage(),
		DestinationID: destinationID,
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

// ConfirmDelivery records the outcome of a pushed notification.
//
// It is a client->server callback carrying the reminder owner's identity (the
// client builds that metadata from the payload's user_id). Guarded at
// CLEARANCE_REGISTERED like the other reminder RPCs, and — like every other
// reminder RPC — scoped to the reminder's OWNER: a confirmation for someone
// else's reminder is NotFound.
//
// Only a reminder currently in SENT is advanced, so a duplicate or retried
// confirmation is a harmless no-op:
//
//   - delivered=false                 -> FAILED
//   - delivered=true, one-shot        -> DELIVERED (kept, not deleted)
//   - delivered=true, repeating       -> datetime advanced to the next
//     occurrence in the reminder's timezone, status back to PENDING
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

	// Reminders are private to their creator, and this method MUTATES one. Same
	// check and same code as GetReminder: NotFound rather than PermissionDenied,
	// so the response does not confirm that the id exists.
	//
	// Without it, anyone at CLEARANCE_REGISTERED holding an id could — for the
	// whole window a reminder sits in SENT — suppress it by confirming a failure,
	// move a repeating reminder's next fire time, probe which ids exist, and have
	// a forged REMINDER_DELIVERED recorded against its owner. The production
	// client already sends the owner's identity, so this changes no real caller.
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

	// A repeating reminder reschedules; a one-shot is marked delivered. The
	// returned bool distinguishes a real transition from the guarded no-op, and
	// the analytics write below depends on it.
	var advanced bool
	if reminderRow.RepeatCron != nil && *reminderRow.RepeatCron != "" {
		loc := reminderLocation(reminderRow.Timezone)
		next, nextErr := reminder.NextOccurrence(*reminderRow.RepeatCron, time.Now(), loc)
		if nextErr != nil {
			// The schedule was valid at create/update time; if it somehow no
			// longer yields a next fire, close the reminder out rather than leave
			// it stuck at SENT.
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

	// Nothing moved: this confirmation is a duplicate or arrived after a reclaim
	// re-claimed the reminder. That is not an error — the caller did its job — but
	// it must NOT produce a second analytics row.
	//
	// It happens without an attacker: two ginbot-discord instances during a
	// rolling restart both receive the fan-out, both post, and both confirm. An
	// unconditional write made "reminders delivered" count confirmations rather
	// than deliveries, unbounded per reminder.
	if !advanced {
		logConfirmNoOp(req.GetId())
		return connect.NewResponse(pb.ConfirmDeliveryResp_builder{}.Build()), nil
	}

	// Analytics: record the delivery against the reminder's owner. Best-effort.
	if err := db.CreateActionRecord(ctx, pb.ActionType_ACTION_TYPE_REMINDER_DELIVERED, *reminderRow.UserID, nil); err != nil {
		log.Z.Error("failed to record reminder delivery", zap.Error(err))
	}

	return connect.NewResponse(pb.ConfirmDeliveryResp_builder{}.Build()), nil
}

// logConfirmNoOp records a confirmation that changed nothing. Debug, not warn:
// a duplicate confirm is expected during a rolling restart and is exactly what
// the SENT-only guard exists to absorb.
func logConfirmNoOp(reminderID string) {
	log.Z.Debug("confirmation did not advance a reminder; it was not in SENT",
		zap.String("reminder_id", reminderID))
}

// isDMDestination reports whether a reminder destination is a direct message
// rather than a shared space. A DM carries no instance_uid; the lower repeat
// floor (10m) applies to it, the higher (12h) to everything with an instance.
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

// reminderLocation resolves a reminder's stored IANA timezone, defaulting to UTC
// when it is empty or unknown so a repeat can always be rescheduled.
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
