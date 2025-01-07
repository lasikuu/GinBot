package service

import (
	"github.com/lasikuu/GinBot/pkg/grpc/server"
)

var ReverseServer *server.ReverseServer
var InstanceServer *server.InstanceServer
var UserServer *server.UserServer
var UtilityServer *server.UtilityServer
var ReminderServer *server.ReminderServer
var AnalyticsServer *server.AnalyticsServer
var EntertainmentServer *server.EntertainmentServer

func InitServices() {
	ReverseServer = server.NewReverseServer()
	InstanceServer = server.NewInstanceServer()
	UserServer = server.NewUserServer()
	UtilityServer = server.NewUtilityServer()
	ReminderServer = server.NewReminderServer()
	AnalyticsServer = server.NewAnalyticsServer()
	EntertainmentServer = server.NewEntertainmentServer()
}
