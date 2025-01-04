package server

import (
	"context"

	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/proto"
)

type ReminderServer struct {
	pb.UnimplementedReminderServiceServer
}

func NewReminderServer() *ReminderServer {
	s := &ReminderServer{}
	return s
}

func (s *ReminderServer) GetReminder(ctx context.Context, req *pb.GetReminderReq) (*pb.GetReminderResp, error) {
	userInfo, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	reminder, err := db.GetReminder(req.Id, &userInfo.ID)
	if err != nil {
		return nil, err
	}

	return &pb.GetReminderResp{
		Reminder: reminder,
	}, nil
}

func (s *ReminderServer) ListReminders(ctx context.Context, req *pb.ListRemindersReq) (*pb.ListRemindersResp, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (s *ReminderServer) CreateReminder(ctx context.Context, req *pb.CreateReminderReq) (*pb.CreateReminderResp, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (s *ReminderServer) DeleteReminder(ctx context.Context, req *pb.DeleteReminderReq) (*pb.DeleteReminderResp, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (s *ReminderServer) UpdateReminder(ctx context.Context, req *pb.UpdateReminderReq) (*pb.UpdateReminderResp, error) {
	_, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	return nil, nil
}
