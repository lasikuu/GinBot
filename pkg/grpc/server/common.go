package server

import (
	"context"
	"strconv"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ContextMetadata struct {
	PlatformEnum pb.Platform
	PlatformUID  *string
}

// getMetadata extracts metadata from the context.
// The same key can have multiple values. We only care about the first one.
func getMetadata(ctx context.Context) (*ContextMetadata, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.DataLoss, "failed to get metadata")
	}
	platformEnums := md.Get("platform_enum")
	if len(platformEnums) == 0 || platformEnums[0] == "" {
		return nil, status.Errorf(codes.InvalidArgument, "platform_enum is not provided")
	}

	platformEnum, err := strconv.ParseInt(platformEnums[0], 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse platform_enum")
	}

	userIDs := md.Get("user_id")
	var userID *string
	if len(userIDs) == 0 || userIDs[0] == "" {
		userID = nil
	} else {
		userID = &userIDs[0]
	}

	return &ContextMetadata{
		PlatformEnum: pb.Platform(platformEnum),
		PlatformUID:  userID,
	}, nil
}
