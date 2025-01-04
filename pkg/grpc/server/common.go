package server

import (
	"context"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type UserInfo struct {
	ID        int64
	Clearance int8
}

func getMetadata(ctx context.Context) (*UserInfo, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.DataLoss, "failed to get metadata.")
	}
	userIDs := md.Get("user_id")
	clearances := md.Get("user_clearance")

	if len(userIDs) == 0 || len(clearances) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "user_id or user_clearance is not provided.")
	}

	userID, err := strconv.ParseInt(userIDs[0], 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse user_id.")
	}

	clearance, err := strconv.ParseInt(clearances[0], 10, 8)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse user_clearance.")
	}

	return &UserInfo{
		ID:        userID,
		Clearance: int8(clearance),
	}, nil
}
