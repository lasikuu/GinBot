package service

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
)

var ReverseServer *server.ReverseServer
var InstanceServer *server.InstanceServer
var UserServer *server.UserServer
var UtilityServer *server.UtilityServer
var ReminderServer *server.ReminderServer
var AnalyticsServer *server.AnalyticsServer
var EntertainmentServer *server.EntertainmentServer
var TriggerServer *server.TriggerServer
var RepostServer *server.RepostServer

// Mount is one service ready to be attached to an http.ServeMux, as the
// generated New<X>ServiceHandler constructors return it.
type Mount struct {
	Name    string
	Path    string
	Handler http.Handler
}

// Handlers builds every service this server mounts, in one place.
//
// It exists so that what is mounted, what reflection advertises, what health
// checks answer for, and what the authorization coverage test walks are all
// ONE list rather than four that agree by hand. That is not tidiness: the
// clearance Requirements map is default-open, so a service mounted but missing
// from the name list would have every one of its procedures invisible to the
// coverage test and therefore silently public. Deriving the names from the same
// pairs that get mounted makes that particular drift unrepresentable.
//
// DiscordService is deliberately absent: its server implementation does not
// exist yet, so mounting it would advertise an RPC surface nothing serves.
func Handlers(opts []connect.HandlerOption) []Mount {
	mount := func(name string, path string, handler http.Handler) Mount {
		return Mount{Name: name, Path: path, Handler: handler}
	}

	instancePath, instanceHandler := ginbotv1connect.NewInstanceServiceHandler(InstanceServer, opts...)
	userPath, userHandler := ginbotv1connect.NewUserServiceHandler(UserServer, opts...)
	utilityPath, utilityHandler := ginbotv1connect.NewUtilityServiceHandler(UtilityServer, opts...)
	reminderPath, reminderHandler := ginbotv1connect.NewReminderServiceHandler(ReminderServer, opts...)
	analyticsPath, analyticsHandler := ginbotv1connect.NewAnalyticsServiceHandler(AnalyticsServer, opts...)
	entertainmentPath, entertainmentHandler := ginbotv1connect.NewEntertainmentServiceHandler(EntertainmentServer, opts...)
	reversePath, reverseHandler := ginbotv1connect.NewReverseServiceHandler(ReverseServer, opts...)
	triggerPath, triggerHandler := ginbotv1connect.NewTriggerServiceHandler(TriggerServer, opts...)
	repostPath, repostHandler := ginbotv1connect.NewRepostServiceHandler(RepostServer, opts...)

	return []Mount{
		mount(ginbotv1connect.InstanceServiceName, instancePath, instanceHandler),
		mount(ginbotv1connect.UserServiceName, userPath, userHandler),
		mount(ginbotv1connect.UtilityServiceName, utilityPath, utilityHandler),
		mount(ginbotv1connect.ReminderServiceName, reminderPath, reminderHandler),
		mount(ginbotv1connect.AnalyticsServiceName, analyticsPath, analyticsHandler),
		mount(ginbotv1connect.EntertainmentServiceName, entertainmentPath, entertainmentHandler),
		mount(ginbotv1connect.ReverseServiceName, reversePath, reverseHandler),
		mount(ginbotv1connect.TriggerServiceName, triggerPath, triggerHandler),
		mount(ginbotv1connect.RepostServiceName, repostPath, repostHandler),
	}
}

// RegisteredServiceNames is the fully-qualified ginbot.v1 service names that
// cmd/ginbot-server mounts, derived from Handlers so the two cannot disagree.
//
// It is safe to call before InitServices: the constructors only read the
// package-level server pointers to bind them into a handler, and nothing here
// looks at the name of anything but the generated constant.
func RegisteredServiceNames() []string {
	mounts := Handlers(nil)

	names := make([]string, 0, len(mounts))
	for _, m := range mounts {
		names = append(names, m.Name)
	}

	return names
}

// InitServices constructs every mounted service implementation. healthProbe is
// threaded to UtilityServer so UtilityService/HealthCheck, the gRPC health
// protocol and the plain GET /healthz endpoint cmd/ginbot-server wires up all
// answer from the same probe.
func InitServices(healthProbe server.HealthProbe) {
	ReverseServer = server.NewReverseServer()
	InstanceServer = server.NewInstanceServer()
	UserServer = server.NewUserServer()
	UtilityServer = server.NewUtilityServer(healthProbe)
	ReminderServer = server.NewReminderServer()
	AnalyticsServer = server.NewAnalyticsServer()
	EntertainmentServer = server.NewEntertainmentServer()
	TriggerServer = server.NewTriggerServer()
	RepostServer = server.NewRepostServer()
}
