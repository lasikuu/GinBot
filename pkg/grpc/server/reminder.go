package server

import (
	"context"
	"errors"
	"time"

	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ReminderServer struct {
	pb.UnimplementedReminderServiceServer
}

func NewReminderServer() *ReminderServer {
	s := &ReminderServer{}
	return s
}

// callerUser resolves the caller's platform identity to a user_account row.
// Returns FailedPrecondition when the caller has not registered.
func callerUser(ctx context.Context, meta *ContextMetadata) (*model.User, error) {
	if meta.PlatformUID == nil {
		return nil, status.Errorf(codes.InvalidArgument, "user_id metadata is required")
	}

	user, err := db.GetUserByPlatformUID(ctx, meta.PlatformEnum, *meta.PlatformUID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, status.Errorf(codes.FailedPrecondition, "caller is not registered")
	}
	if err != nil {
		log.Z.Error("failed to resolve caller", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to resolve caller")
	}

	return user, nil
}

func (s *ReminderServer) GetReminder(ctx context.Context, req *pb.GetReminderReq) (*pb.GetReminderResp, error) {
	meta, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, status.Errorf(codes.InvalidArgument, "id is required")
	}

	caller, err := callerUser(ctx, meta)
	if err != nil {
		return nil, err
	}

	reminder, err := db.GetReminder(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "reminder not found")
	}
	if err != nil {
		log.Z.Error("failed to get reminder", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get reminder")
	}

	// Reminders are private to their creator. NotFound rather than
	// PermissionDenied so the response does not confirm that the id exists.
	if reminder.UserID == nil || *reminder.UserID != caller.ID {
		return nil, status.Errorf(codes.NotFound, "reminder not found")
	}

	destination, err := db.GetReminderDestination(ctx, reminder.DestinationID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		log.Z.Error("failed to resolve reminder destination", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to resolve reminder destination")
	}

	return pb.GetReminderResp_builder{
		Reminder: reminder.ToProto(destination),
	}.Build(), nil
}

func (s *ReminderServer) ListReminders(ctx context.Context, _ *pb.ListRemindersReq) (*pb.ListRemindersResp, error) {
	if _, err := getMetadata(ctx); err != nil {
		return nil, err
	}

	return nil, status.Error(codes.Unimplemented, "ListReminders is not implemented yet")
}

func (s *ReminderServer) CreateReminder(ctx context.Context, req *pb.CreateReminderReq) (*pb.CreateReminderResp, error) {
	meta, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	// The validation interceptor covers these too, but it is wired only in
	// cmd/ginbot-server and only for unary RPCs, and AGENTS.md requires handlers
	// to check required fields themselves.
	if !(req.HasDatetime() && req.HasTimezone()) {
		return nil, status.Errorf(codes.InvalidArgument, "datetime and timezone are required")
	}

	if !(req.HasDestination() &&
		req.GetDestination().HasPlatformEnum() &&
		req.GetDestination().HasInstanceMeta() &&
		req.GetDestination().HasDestinationMeta()) {
		return nil, status.Errorf(codes.InvalidArgument, "destination is required")
	}

	caller, err := callerUser(ctx, meta)
	if err != nil {
		return nil, err
	}

	destinationID, err := db.GetOrCreateDestinationByMeta(ctx, req.GetDestination())
	if err != nil {
		log.Z.Error("failed to get or create destination", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to resolve destination")
	}

	reminderID, err := db.CreateReminder(ctx, req, caller.ID, destinationID)
	if err != nil {
		log.Z.Error("failed to create reminder", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to create reminder")
	}

	return pb.CreateReminderResp_builder{
		Id: &reminderID,
	}.Build(), nil
}

func (s *ReminderServer) DeleteReminder(ctx context.Context, _ *pb.DeleteReminderReq) (*emptypb.Empty, error) {
	if _, err := getMetadata(ctx); err != nil {
		return nil, err
	}

	return nil, status.Error(codes.Unimplemented, "DeleteReminder is not implemented yet")
}

func (s *ReminderServer) UpdateReminder(ctx context.Context, _ *pb.UpdateReminderReq) (*emptypb.Empty, error) {
	if _, err := getMetadata(ctx); err != nil {
		return nil, err
	}

	return nil, status.Error(codes.Unimplemented, "UpdateReminder is not implemented yet")
}

func (s *ReminderServer) GetExpiredReminders(ctx context.Context, _ *emptypb.Empty) (*pb.GetExpiredRemindersResp, error) {
	if _, err := getMetadata(ctx); err != nil {
		return nil, err
	}

	reminders, err := db.ExpiredReminders(ctx, time.Now())
	if err != nil {
		log.Z.Error("failed to list expired reminders", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to list expired reminders")
	}

	out := make([]*pb.Reminder, 0, len(reminders))
	for _, reminder := range reminders {
		destination, err := db.GetReminderDestination(ctx, reminder.DestinationID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			log.Z.Error("failed to resolve reminder destination", zap.Error(err))
			return nil, status.Errorf(codes.Internal, "failed to resolve reminder destination")
		}
		out = append(out, reminder.ToProto(destination))
	}

	return pb.GetExpiredRemindersResp_builder{
		Reminders: out,
	}.Build(), nil
}
