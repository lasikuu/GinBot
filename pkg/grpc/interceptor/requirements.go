package interceptor

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
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
// ReverseService/OpenClientActionStream is absent for a different reason: it is
// a stream, and this map only drives the unary interceptor. Adding it here
// would read as protection that does not exist.
//
// Every key is a generated constant. Never write one out by hand: a key that no
// longer matches the generated method name compiles fine and quietly turns the
// RPC public.
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
		pb.AnalyticsService_CreateActionRecord_FullMethodName: registered,
		pb.AnalyticsService_ListActionRecords_FullMethodName:  registered,

		// Above the CLEARANCE_REGISTERED baseline on purpose: this changes the
		// bot's presence for everyone who can see it, so it is not a per-user
		// setting however cheap the call looks. The service is not registered on
		// the server yet, so the floor is in place before the method exists.
		pb.DiscordService_SetDiscordActivityType_FullMethodName: administrator,

		pb.EntertainmentService_SetBirthday_FullMethodName: registered,

		pb.InstanceService_GetInstance_FullMethodName:   registered,
		pb.InstanceService_ListInstances_FullMethodName: registered,
		// Instance administration reconfigures a whole guild or room, so it is
		// held to the highest bar in this map.
		pb.InstanceService_CreateInstance_FullMethodName: administrator,
		pb.InstanceService_UpdateInstance_FullMethodName: administrator,
		pb.InstanceService_DeleteInstance_FullMethodName: administrator,

		pb.ReminderService_GetReminder_FullMethodName:    registered,
		pb.ReminderService_ListReminders_FullMethodName:  registered,
		pb.ReminderService_CreateReminder_FullMethodName: registered,
		pb.ReminderService_UpdateReminder_FullMethodName: registered,
		pb.ReminderService_DeleteReminder_FullMethodName: registered,
		// ConfirmDelivery is a client->server callback carrying the reminder
		// owner's identity. It is default-open unless listed here, which would be
		// a silent security hole, so it is held to the same floor as the rest of
		// the reminder surface.
		pb.ReminderService_ConfirmDelivery_FullMethodName: registered,

		pb.RepostService_CheckRepost_FullMethodName: registered,

		pb.TriggerService_TryTrigger_FullMethodName:      registered,
		pb.TriggerService_ExecTrigger_FullMethodName:     registered,
		pb.TriggerService_GetTrigger_FullMethodName:      registered,
		pb.TriggerService_ListTriggers_FullMethodName:    registered,
		pb.TriggerService_CreateTrigger_FullMethodName:   registered,
		pb.TriggerService_UpdateTrigger_FullMethodName:   registered,
		pb.TriggerService_DeleteTrigger_FullMethodName:   registered,
		pb.TriggerService_GetTriggerStats_FullMethodName: registered,
		pb.TriggerService_GetFile_FullMethodName:         registered,

		pb.UserService_GetUser_FullMethodName:                   registered,
		pb.UserService_GetCongratulableBirthdays_FullMethodName: registered,
		pb.UserService_SetLocale_FullMethodName:                 registered,
		pb.UserService_SetTimezone_FullMethodName:               registered,
	}
}
