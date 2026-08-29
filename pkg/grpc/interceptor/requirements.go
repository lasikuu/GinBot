package interceptor

import (
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
)

// DefaultRequirements is the minimum clearance for every guarded RPC.
// UserService/Register, UtilityService/HealthCheck, UtilityService/Ping and
// EntertainmentService/GetRandomNumber are deliberately absent, so public.
func DefaultRequirements() Requirements {
	const (
		registered    = pb.Clearance_CLEARANCE_REGISTERED
		administrator = pb.Clearance_CLEARANCE_ADMINISTRATOR
	)

	return Requirements{
		ginbotv1connect.AnalyticsServiceCreateActionRecordProcedure: registered,
		ginbotv1connect.AnalyticsServiceListActionRecordsProcedure:  registered,

		// Changes the bot's presence for everyone, not a per-user setting.
		ginbotv1connect.DiscordServiceSetDiscordActivityTypeProcedure: administrator,

		ginbotv1connect.EntertainmentServiceSetBirthdayProcedure: registered,

		ginbotv1connect.InstanceServiceGetInstanceProcedure:   registered,
		ginbotv1connect.InstanceServiceListInstancesProcedure: registered,
		// Instance administration reconfigures a whole guild or room.
		ginbotv1connect.InstanceServiceCreateInstanceProcedure: administrator,
		ginbotv1connect.InstanceServiceUpdateInstanceProcedure: administrator,
		ginbotv1connect.InstanceServiceDeleteInstanceProcedure: administrator,

		ginbotv1connect.ReminderServiceGetReminderProcedure:    registered,
		ginbotv1connect.ReminderServiceListRemindersProcedure:  registered,
		ginbotv1connect.ReminderServiceCreateReminderProcedure: registered,
		ginbotv1connect.ReminderServiceUpdateReminderProcedure: registered,
		ginbotv1connect.ReminderServiceDeleteReminderProcedure: registered,
		// A client->server callback, guarded like the rest of the surface.
		ginbotv1connect.ReminderServiceConfirmDeliveryProcedure: registered,

		ginbotv1connect.RepostServiceCheckRepostProcedure: registered,

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
