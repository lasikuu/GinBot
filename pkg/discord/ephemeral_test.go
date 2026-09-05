package discord

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// GetTrigger extends fakeTriggerClient (triggers_test.go) so the table below
// can invoke triggerInfo.
func (f *fakeTriggerClient) GetTrigger(_ context.Context, in *connect.Request[pb.GetTriggerReq]) (*connect.Response[pb.GetTriggerResp], error) {
	id := in.Msg.GetId()
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(pb.GetTriggerResp_builder{
		Trigger: pb.Trigger_builder{Id: &id, Phrase: strPtr("gm")}.Build(),
	}.Build()), nil
}

func strPtr(s string) *string { return &s }

// DeleteTrigger extends fakeTriggerClient the same way GetTrigger does above.
func (f *fakeTriggerClient) DeleteTrigger(_ context.Context, _ *connect.Request[pb.DeleteTriggerReq]) (*connect.Response[pb.DeleteTriggerResp], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(pb.DeleteTriggerResp_builder{}.Build()), nil
}

type fakeUserClient struct {
	ginbotv1connect.UserServiceClient
}

func (f *fakeUserClient) GetUser(_ context.Context, _ *connect.Request[pb.GetUserReq]) (*connect.Response[pb.GetUserResp], error) {
	id := "018f0000-0000-7000-8000-0000000000aa"
	return connect.NewResponse(pb.GetUserResp_builder{User: pb.User_builder{Id: &id}.Build()}.Build()), nil
}

func (f *fakeUserClient) Register(_ context.Context, _ *connect.Request[pb.RegisterReq]) (*connect.Response[pb.RegisterResp], error) {
	return connect.NewResponse(pb.RegisterResp_builder{}.Build()), nil
}

func (f *fakeUserClient) SetLocale(_ context.Context, _ *connect.Request[pb.SetLocaleReq]) (*connect.Response[pb.SetLocaleResp], error) {
	return connect.NewResponse(pb.SetLocaleResp_builder{}.Build()), nil
}

func (f *fakeUserClient) SetTimezone(_ context.Context, _ *connect.Request[pb.SetTimezoneReq]) (*connect.Response[pb.SetTimezoneResp], error) {
	return connect.NewResponse(pb.SetTimezoneResp_builder{}.Build()), nil
}

type fakeReminderClient struct {
	ginbotv1connect.ReminderServiceClient

	// listResp, when set, is returned by ListReminders instead of the empty
	// default; used to exercise the non-empty listing path.
	listResp *pb.ListRemindersResp
}

func (f *fakeReminderClient) GetReminder(_ context.Context, in *connect.Request[pb.GetReminderReq]) (*connect.Response[pb.GetReminderResp], error) {
	id := in.Msg.GetId()
	return connect.NewResponse(pb.GetReminderResp_builder{
		Reminder: pb.Reminder_builder{Id: &id}.Build(),
	}.Build()), nil
}

func (f *fakeReminderClient) ListReminders(_ context.Context, _ *connect.Request[pb.ListRemindersReq]) (*connect.Response[pb.ListRemindersResp], error) {
	if f.listResp != nil {
		return connect.NewResponse(f.listResp), nil
	}

	return connect.NewResponse(pb.ListRemindersResp_builder{}.Build()), nil
}

// ephemeralTestContext carries an invoker (for register) and the fakes every
// command under test needs.
func ephemeralTestContext() context.Context {
	ctx := withOrigin(context.Background(), "guild-1", "channel-1")
	ctx = withInvoker(ctx, &discordgo.User{ID: "disc-1", Username: "kohana"})
	return withClients(ctx, &client.Clients{
		User:     &fakeUserClient{},
		Reminder: &fakeReminderClient{},
		Trigger:  &fakeTriggerClient{},
	})
}

// TestEphemeralCommandsRespondEphemerallyOnSuccess: Command.Ephemeral is read
// before the handler runs to decide a deferred acknowledgement's visibility,
// so it must agree with what the handler itself actually returns on success.
func TestEphemeralCommandsRespondEphemerallyOnSuccess(t *testing.T) {
	commandRegistry = newTestRegistry(t)
	ctx := ephemeralTestContext()

	tests := []struct {
		name string
		cmd  command.Command
		args map[string]any
	}{
		{name: "userinfo", cmd: userInfoCommand()},
		{name: "register", cmd: registerCommand()},
		{name: "locale", cmd: localeCommand(), args: map[string]any{"locale": "en"}},
		{name: "timezone", cmd: timezoneCommand(), args: map[string]any{"timezone": "UTC"}},
		{name: "triggerinfo", cmd: triggerInfoCommand(), args: map[string]any{"id": triggerID}},
		{name: "triggerstats", cmd: triggerStatsCommand()},
		{name: "triggers", cmd: triggerListCommand()},
		{name: "triggeradd", cmd: triggerAddCommand(), args: map[string]any{"phrase": "gm", "reply": "hi"}},
		{name: "triggerdel", cmd: triggerDelCommand(), args: map[string]any{"id": triggerID}},
		{name: "triggermod", cmd: triggerModCommand(), args: map[string]any{"id": triggerID, "reply": "hi"}},
		{name: "reminderinfo", cmd: reminderInfoCommand(), args: map[string]any{"id": triggerID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.cmd.Ephemeral {
				t.Fatalf("test premise broken: %s must declare Ephemeral: true for this table to mean anything", tt.cmd.Name)
			}

			resp, err := invokeNamed(t, tt.cmd, ctx, tt.args)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if !resp.Ephemeral {
				t.Errorf("%s: success response Ephemeral = false, want true to match the declared command", tt.cmd.Name)
			}
		})
	}
}
