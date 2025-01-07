package server

import (
	"context"
	"time"

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

func (s *ReminderServer) GetReminder(ctx context.Context, req *pb.GetReminderReq) (*pb.GetReminderResp, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, status.Errorf(codes.InvalidArgument, "id is required")
	}

	reminder, err := db.GetReminder(req.GetId())
	if err != nil {
		return nil, err
	}

	return pb.GetReminderResp_builder{
		Reminder: reminder,
	}.Build(), nil
}

func (s *ReminderServer) ListReminders(ctx context.Context, req *pb.ListRemindersReq) (*pb.ListRemindersResp, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (s *ReminderServer) CreateReminder(ctx context.Context, req *pb.CreateReminderReq) (*pb.CreateReminderResp, error) {
	// TODO: Refactor and generalize metadata and field checks, and error handling
	meta, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}
	if meta.PlatformUID == nil {
		return nil, status.Errorf(codes.InvalidArgument, "platform_uid is required")
	}

	if !(req.HasDestination() &&
		req.GetDestination().HasPlatformEnum() &&
		req.GetDestination().HasInstanceMeta() &&
		req.GetDestination().HasDestinationMeta()) {
		return nil, status.Errorf(codes.InvalidArgument, "destination is required")
	}

	destinationID, err := db.GetOrCreateDestinationByMeta(req.GetDestination())
	if err != nil {
		log.Z.Error("failed to get destination by meta", zap.Error(err))
		return nil, err
	}

	userID, _, err := db.GetUserByPlatformUID(meta.PlatformEnum, *meta.PlatformUID)
	if err != nil {
		log.Z.Error("failed to get user by platform uid", zap.Error(err))
		return nil, err
	}

	reminderID, err := db.CreateReminder(req, userID, destinationID)

	return pb.CreateReminderResp_builder{
		Id: &reminderID,
	}.Build(), nil
}

func (s *ReminderServer) DeleteReminder(ctx context.Context, req *pb.DeleteReminderReq) (*emptypb.Empty, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (s *ReminderServer) UpdateReminder(ctx context.Context, req *pb.UpdateReminderReq) (*emptypb.Empty, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (s *ReminderServer) GetExpiredReminders(ctx context.Context, _ *emptypb.Empty) (*pb.GetExpiredRemindersResp, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	expiredReminders, err := db.ExpiredReminders(&now)
	if err != nil {
		return nil, err
	}

	return pb.GetExpiredRemindersResp_builder{
		Reminders: expiredReminders,
	}.Build(), nil
}
