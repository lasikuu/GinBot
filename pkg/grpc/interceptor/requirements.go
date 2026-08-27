package interceptor

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
)

// DefaultRequirements is the minimum clearance for every guarded RPC.
//
// Four methods are deliberately absent, and therefore public:
//
//   - UserService/Register — the caller has no account yet by definition, so
//     requiring one would make registration unreachable.
//   - UtilityService/HealthCheck — monitoring, exposes no user data.
//   - UtilityService/Ping — liveness, exposes no user data.
//   - EntertainmentService/GetRandomNumber — reads nothing and stores nothing.
//
// Every key is a generated ginbotv1connect.*Procedure constant. Never write one
// out by hand: a key that no longer matches the generated procedure string
// compiles fine and quietly turns the RPC public.
//
// Known gap: nothing in the product grants clearance above CLEARANCE_REGISTERED.
// Registration sets that level and there is no promotion RPC, command or admin
// surface, so every floor above it — CLEARANCE_MODERATOR on reading another
// user's row, CLEARANCE_ADMINISTRATOR on the Create/Update/DeleteInstance
// methods below — is unreachable except by editing user_account.clearance
// directly in psql. That is accepted for now: the floors are correct and are
// enforced, and building a grant mechanism is a separate piece of work. They are
// not dead code, they are simply only exercisable by an operator.
func DefaultRequirements() Requirements {
	const (
		registered    = pb.Clearance_CLEARANCE_REGISTERED
		administrator = pb.Clearance_CLEARANCE_ADMINISTRATOR
	)

	return Requirements{
		ginbotv1connect.AnalyticsServiceCreateActionRecordProcedure: registered,
		ginbotv1connect.AnalyticsServiceListActionRecordsProcedure:  registered,

		// Above the CLEARANCE_REGISTERED baseline on purpose: this changes the
		// bot's presence for everyone who can see it, so it is not a per-user
		// setting however cheap the call looks. The service is not registered on
		// the server yet, so the floor is in place before the method exists.
		ginbotv1connect.DiscordServiceSetDiscordActivityTypeProcedure: administrator,

		ginbotv1connect.EntertainmentServiceSetBirthdayProcedure: registered,

		ginbotv1connect.InstanceServiceGetInstanceProcedure:   registered,
		ginbotv1connect.InstanceServiceListInstancesProcedure: registered,
		// Instance administration reconfigures a whole guild or room, so it is
		// held to the highest bar in this map.
		ginbotv1connect.InstanceServiceCreateInstanceProcedure: administrator,
		ginbotv1connect.InstanceServiceUpdateInstanceProcedure: administrator,
		ginbotv1connect.InstanceServiceDeleteInstanceProcedure: administrator,

		ginbotv1connect.ReminderServiceGetReminderProcedure:    registered,
		ginbotv1connect.ReminderServiceListRemindersProcedure:  registered,
		ginbotv1connect.ReminderServiceCreateReminderProcedure: registered,
		ginbotv1connect.ReminderServiceUpdateReminderProcedure: registered,
		ginbotv1connect.ReminderServiceDeleteReminderProcedure: registered,
		// ConfirmDelivery is a client->server callback carrying the reminder
		// owner's identity. It is default-open unless listed here, which would be
		// a silent security hole, so it is held to the same floor as the rest of
		// the reminder surface.
		ginbotv1connect.ReminderServiceConfirmDeliveryProcedure: registered,

		ginbotv1connect.RepostServiceCheckRepostProcedure: registered,

		// A stream, not a unary call, but it goes through exactly the same
		// ClearanceInterceptor.WrapStreamingHandler and is held to exactly the
		// same map — see ADR-0012 and the comment on maxStreamClients in
		// pkg/grpc/server/reverse.go for what this closes.
		ginbotv1connect.ReverseServiceOpenClientActionStreamProcedure: registered,

		ginbotv1connect.TriggerServiceTryTriggerProcedure:      registered,
		ginbotv1connect.TriggerServiceExecTriggerProcedure:     registered,
		ginbotv1connect.TriggerServiceGetTriggerProcedure:      registered,
		ginbotv1connect.TriggerServiceListTriggersProcedure:    registered,
		ginbotv1connect.TriggerServiceCreateTriggerProcedure:   registered,
		ginbotv1connect.TriggerServiceUpdateTriggerProcedure:   registered,
		ginbotv1connect.TriggerServiceDeleteTriggerProcedure:   registered,
		ginbotv1connect.TriggerServiceGetTriggerStatsProcedure: registered,
		ginbotv1connect.TriggerServiceGetFileProcedure:         registered,

		ginbotv1connect.UserServiceGetUserProcedure:                   registered,
		ginbotv1connect.UserServiceGetCongratulableBirthdaysProcedure: registered,
		ginbotv1connect.UserServiceSetLocaleProcedure:                 registered,
		ginbotv1connect.UserServiceSetTimezoneProcedure:               registered,
	}
}
