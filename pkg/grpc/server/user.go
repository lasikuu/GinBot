package server

import (
	"context"
	"errors"
	"time"

	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// localTimezone is the one name time.LoadLocation accepts that means nothing to
// a caller: it resolves to whatever zone the server process happens to run in.
const localTimezone = "Local"

type UserServer struct {
	pb.UnimplementedUserServiceServer
}

func NewUserServer() *UserServer {
	s := &UserServer{}
	return s
}

func (s *UserServer) Register(ctx context.Context, req *pb.RegisterReq) (*pb.RegisterResp, error) {
	// Platform identity comes from metadata, never from the request. It used to
	// be available as two request fields as well; those numbers are now reserved
	// in the proto, so there is one channel for identity instead of two that
	// could disagree.
	//
	// That is not authentication. The client asserts the identity and this server
	// trusts it, so anything that can reach the gRPC port can register an account
	// against any platform uid it likes. See pkg/grpc/callermeta.
	meta, err := getMetadata(ctx)
	if err != nil {
		return nil, err
	}

	if meta.PlatformUID == nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s metadata is required", callermeta.KeyUserID)
	}

	if !req.HasUsername() {
		return nil, status.Errorf(codes.InvalidArgument, "username is required")
	}

	userID, err := db.CreateUser(
		ctx,
		req.GetUsername(),
		meta.PlatformEnum,
		*meta.PlatformUID,
		req.GetPlatformMetadata(),
		req.GetLocale(),
	)
	if errors.Is(err, db.ErrAlreadyExists) {
		return nil, status.Errorf(codes.AlreadyExists, "this platform identity is already registered")
	}
	if err != nil {
		log.Z.Error("failed to create user", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to create user")
	}

	return pb.RegisterResp_builder{
		UserId: &userID,
	}.Build(), nil
}

func (s *UserServer) GetUser(ctx context.Context, req *pb.GetUserReq) (*pb.GetUserResp, error) {
	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	// An unset id means "me". A platform client never learns its own
	// user_account UUID — identity travels as a platform id — so without this
	// there is no way for a caller to read its own row.
	if !req.HasId() || req.GetId() == caller.ID {
		return pb.GetUserResp_builder{
			User: caller.ToProto(),
		}.Build(), nil
	}

	// A user row carries locale, timezone and birthday, so reading somebody
	// else's is a moderator action.
	if caller.Clearance < int32(pb.Clearance_CLEARANCE_MODERATOR) {
		return nil, status.Errorf(codes.PermissionDenied, "cannot read another user")
	}

	user, err := db.GetUser(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "user not found")
	}
	if err != nil {
		log.Z.Error("failed to get user", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get user")
	}

	return pb.GetUserResp_builder{
		User: user.ToProto(),
	}.Build(), nil
}

func (s *UserServer) SetLocale(ctx context.Context, req *pb.SetLocaleReq) (*emptypb.Empty, error) {
	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	// The request carries no subject, so the caller is the subject. The allowed
	// values are pinned by a protovalidate pattern in user.proto, but an absent
	// locale passes that pattern — a rule on a field with presence is skipped
	// when the field is unset — so the handler has to require it itself.
	if !req.HasLocale() || req.GetLocale() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "locale is required")
	}

	if err := db.SetUserLocale(ctx, caller.ID, req.GetLocale()); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "user not found")
		}
		log.Z.Error("failed to set locale", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to set locale")
	}

	return &emptypb.Empty{}, nil
}

func (s *UserServer) SetTimezone(ctx context.Context, req *pb.SetTimezoneReq) (*emptypb.Empty, error) {
	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	// Empty is checked here as well as by the proto's min_len, because
	// time.LoadLocation("") succeeds and returns UTC, so an empty name would
	// otherwise be silently accepted as a valid zone.
	if !req.HasTimezone() || req.GetTimezone() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "timezone is required")
	}

	timezone := req.GetTimezone()

	// protovalidate only checks the name is non-empty, so an unresolvable zone
	// would be stored and then fail much later, when a reminder is scheduled
	// against it. "Local" is rejected as well: it loads successfully but means
	// the server's own zone, which is not what any caller intends.
	if timezone == localTimezone {
		return nil, status.Errorf(codes.InvalidArgument, "timezone must be a named IANA zone, such as Europe/Helsinki")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unknown timezone %q", timezone)
	}

	if err := db.SetUserTimezone(ctx, caller.ID, timezone); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "user not found")
		}
		log.Z.Error("failed to set timezone", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to set timezone")
	}

	return &emptypb.Empty{}, nil
}

func (s *UserServer) GetCongratulableBirthdays(_ context.Context, _ *emptypb.Empty) (*pb.GetCongratulableBirthdaysResp, error) {
	return nil, status.Error(codes.Unimplemented, "GetCongratulableBirthdays is not implemented yet")
}
