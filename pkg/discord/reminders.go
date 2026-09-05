package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/reminder"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler contexts carry no deadline of their own.
const reminderCallTimeout = 20 * time.Second

func boundedReminderCall(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, reminderCallTimeout)
}

const reminderGroup = "reminder"

const (
	reminderSubAdd  = "add"
	reminderSubList = "list"
	reminderSubDel  = "del"
	reminderSubMod  = "mod"
	reminderSubInfo = "info"
)

// The server lists soonest-first, so this shows the reminders about to fire.
const reminderListLimit = 25

const repeatArgDescription = "Repeat schedule: 5-field cron (\"0 9 * * *\"), @daily, or @every 12h"

// Clearing a repeat must be said out loud: an omitted argument means "leave the
// schedule alone", so editing a reminder's text cannot silently unset it.
const clearRepeatSentinel = "none"

func remindCommand() command.Command {
	return command.Command{
		Name:        "remind",
		Aliases:     []string{"remindme", "reminderadd"},
		Description: "Set a reminder, e.g. \"in 2 hours\" \"stretch\"",
		Group:       reminderGroup,
		Sub:         reminderSubAdd,
		Args: []command.Arg{
			{
				// One argument, so a multi-word phrase must be quoted in chat.
				Name:        "when",
				Description: "When to remind, e.g. \"in 2 hours\", 2h30m, \"next tuesday at 9\"",
				Type:        command.ArgString,
				Required:    true,
			},
			{
				Name:        "message",
				Description: "What to remind you about",
				Type:        command.ArgString,
				Required:    true,
			},
			{
				Name:        "repeat",
				Description: repeatArgDescription,
				Type:        command.ArgString,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Slow:      true,
		Ephemeral: true,
		Handler:   remind,
	}
}

func remind(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	whenInput := inv.String("when")
	message := inv.String("message")
	repeat := strings.TrimSpace(inv.String("repeat"))

	loc, timezone := callerLocation(ctx)

	fireAt, err := reminder.ParseWhen(whenInput, time.Now(), loc)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("could not understand the time %q", whenInput))
	}

	destination, err := currentDestination(ctx)
	if err != nil {
		return nil, err
	}

	b := pb.CreateReminderReq_builder{
		Datetime:    timestamppb.New(fireAt),
		Timezone:    &timezone,
		Message:     &message,
		Destination: destination,
	}
	// Nothing to clear at create time, so the sentinel just means "no repeat".
	if repeat != "" && !strings.EqualFold(repeat, clearRepeatSentinel) {
		b.RepeatCron = &repeat
	}

	callCtx, cancel := boundedReminderCall(ctx)
	defer cancel()

	if _, err := clientsFrom(ctx).Reminder.CreateReminder(callCtx, connect.NewRequest(b.Build())); err != nil {
		log.Z.Error("failed to call CreateReminder.", zap.Error(err))
		return nil, err
	}

	content := fmt.Sprintf("Reminder set for %s.", timestampWithRelative(fireAt))
	if b.RepeatCron != nil {
		content += fmt.Sprintf(" Repeats: `%s`.", *b.RepeatCron)
	}

	return &command.Response{Content: content, Ephemeral: true}, nil
}

func remindersCommand() command.Command {
	return command.Command{
		Name:        "reminders",
		Description: "List your reminders",
		Group:       reminderGroup,
		Sub:         reminderSubList,
		Clearance:   pb.Clearance_CLEARANCE_REGISTERED,
		Slow:        true,
		Ephemeral:   true,
		Handler:     listReminders,
	}
}

func listReminders(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	limit := int64(reminderListLimit)
	req := pb.ListRemindersReq_builder{
		Limit: &limit,
	}.Build()

	callCtx, cancel := boundedReminderCall(ctx)
	defer cancel()

	resp, err := clientsFrom(ctx).Reminder.ListReminders(callCtx, connect.NewRequest(req))
	if err != nil {
		log.Z.Error("failed to call ListReminders.", zap.Error(err))
		return nil, err
	}

	reminders := resp.Msg.GetReminders()
	if len(reminders) == 0 {
		return &command.Response{Content: "You have no reminders.", Ephemeral: true}, nil
	}

	var b strings.Builder
	b.WriteString("**Your reminders**\n")
	for _, r := range reminders {
		b.WriteString(renderReminderLine(r))
		b.WriteByte('\n')
	}

	return &command.Response{Content: b.String(), Ephemeral: true, DirectWhenLong: true}, nil
}

func reminderInfoCommand() command.Command {
	return command.Command{
		Name: "reminderinfo",

		Aliases:     []string{"reminderdetails"},
		Description: "Show one of your reminders",
		Group:       reminderGroup,
		Sub:         reminderSubInfo,
		Args: []command.Arg{
			{
				Name:        "id",
				Description: "Reminder id or ref",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Slow:      true,
		Ephemeral: true,
		Handler:   reminderInfo,
	}
}

func reminderInfo(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	id := inv.String("id")
	req := pb.GetReminderReq_builder{Id: &id}.Build()

	callCtx, cancel := boundedReminderCall(ctx)
	defer cancel()

	resp, err := clientsFrom(ctx).Reminder.GetReminder(callCtx, connect.NewRequest(req))
	if err != nil {
		log.Z.Error("failed to call GetReminder.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content:   formatReminderInfo(resp.Msg.GetReminder()),
		Ephemeral: true,
	}, nil
}

// Every timestamp is a Discord tag, so the view reads on the viewer's clock.
// Shows the full uuid, not just the ref: this is where a caller obtains it.
func formatReminderInfo(r *pb.Reminder) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Reminder** `%d`\n", r.GetRef())
	fmt.Fprintf(&b, "Id: `%s`\n", r.GetId())
	fmt.Fprintf(&b, "When: %s\n", renderReminderTime(r))
	fmt.Fprintf(&b, "Status: %s\n", reminderStatusName(r.GetStatus()))
	fmt.Fprintf(&b, "Message: %s\n", emptyDash(r.GetMessage()))
	fmt.Fprintf(&b, "Repeat: %s\n", repeatOrNone(r.GetRepeatCron()))
	fmt.Fprintf(&b, "Destination: %s\n", destinationMention(r.GetDestination()))
	fmt.Fprintf(&b, "Created: %s\n", renderReminderStamp(r.HasCreatedAt(), r.GetCreatedAt().AsTime()))
	fmt.Fprintf(&b, "Updated: %s", renderReminderStamp(r.HasUpdatedAt(), r.GetUpdatedAt().AsTime()))

	return b.String()
}

func repeatOrNone(repeatCron string) string {
	if repeatCron == "" {
		return "none"
	}

	return "`" + repeatCron + "`"
}

// The channel id lives in destination_meta under callermeta's own key. An
// unresolvable channel renders as an em dash rather than a broken `<#>`.
func destinationMention(destination *pb.ReminderDestination) string {
	meta := destination.GetDestinationMeta()
	if meta == nil {
		return emptyDash("")
	}

	value, ok := meta.GetFields()[callermeta.FieldDestinationUID]
	if !ok || value == nil || value.GetStringValue() == "" {
		return emptyDash("")
	}

	return "<#" + value.GetStringValue() + ">"
}

// Relative only, unlike the fire time: an audit line wants "10 minutes ago".
func renderReminderStamp(present bool, instant time.Time) string {
	if !present {
		return emptyDash("")
	}

	return timestampTag(instant, timestampRelative)
}

func reminderDelCommand() command.Command {
	return command.Command{
		Name:        "reminderdel",
		Description: "Delete one or more of your reminders by id",
		Group:       reminderGroup,
		Sub:         reminderSubDel,
		Args: []command.Arg{
			{
				Name:        "ids",
				Description: "Reminder ids or refs, space-separated",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Slow:      true,
		Ephemeral: true,
		Handler:   deleteReminders,
	}
}

// Bounds the whole loop, so N unresponsive ids cannot serialise into N waits of
// reminderCallTimeout each.
const deleteRemindersOverallTimeout = 60 * time.Second

// deleteReminders accepts several space-separated ids; the RPC deletes one.
func deleteReminders(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	ids := strings.Fields(inv.String("ids"))
	if len(ids) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at least one reminder id is required"))
	}

	overallCtx, overallCancel := context.WithTimeout(ctx, deleteRemindersOverallTimeout)
	defer overallCancel()

	clients := clientsFrom(ctx)

	var deleted, failed int
	for _, id := range ids {
		req := pb.DeleteReminderReq_builder{Id: &id}.Build()

		// From overallCtx, not ctx: WithTimeout takes the earlier deadline, so
		// this cannot outlive the loop's own budget.
		callCtx, cancel := boundedReminderCall(overallCtx)
		_, err := clients.Reminder.DeleteReminder(callCtx, connect.NewRequest(req))
		cancel()
		if err != nil {
			// Keep going, so one bad id does not abort the rest.
			failed++
			continue
		}
		deleted++
	}

	content := fmt.Sprintf("Deleted %d reminder(s).", deleted)
	if failed > 0 {
		content += fmt.Sprintf(" %d could not be deleted.", failed)
	}

	return &command.Response{Content: content, Ephemeral: true}, nil
}

func reminderModCommand() command.Command {
	return command.Command{
		Name:        "remindermod",
		Description: "Change a reminder's time, message and repeat",
		Group:       reminderGroup,
		Sub:         reminderSubMod,
		Args: []command.Arg{
			{
				Name:        "id",
				Description: "Reminder id or ref",
				Type:        command.ArgString,
				Required:    true,
			},
			{
				Name:        "when",
				Description: "New time, e.g. \"in 2 hours\", 2h30m",
				Type:        command.ArgString,
				Required:    true,
			},
			{
				Name:        "message",
				Description: "New message",
				Type:        command.ArgString,
				Required:    true,
			},
			{
				// Short on purpose: Discord caps an option description at
				// maxOptionDescription characters.
				Name:        "repeat",
				Description: "New repeat; omit to keep it, \"" + clearRepeatSentinel + "\" to remove it",
				Type:        command.ArgString,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Slow:      true,
		Ephemeral: true,
		Handler:   modifyReminder,
	}
}

func modifyReminder(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	id := inv.String("id")
	whenInput := inv.String("when")
	message := inv.String("message")
	repeat := strings.TrimSpace(inv.String("repeat"))

	loc, timezone := callerLocation(ctx)

	fireAt, err := reminder.ParseWhen(whenInput, time.Now(), loc)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("could not understand the time %q", whenInput))
	}

	destination, err := currentDestination(ctx)
	if err != nil {
		return nil, err
	}

	b := pb.UpdateReminderReq_builder{
		Id:          &id,
		Datetime:    timestamppb.New(fireAt),
		Timezone:    &timezone,
		Message:     &message,
		Destination: destination,
	}

	// repeat_cron is patch-shaped: unset leaves the schedule alone, an explicit
	// empty string clears it.
	repeatNote := ""
	switch {
	case repeat == "":
	case strings.EqualFold(repeat, clearRepeatSentinel):
		cleared := ""
		b.RepeatCron = &cleared
		repeatNote = " Repeat removed."
	default:
		b.RepeatCron = &repeat
		repeatNote = fmt.Sprintf(" Repeats: `%s`.", repeat)
	}

	callCtx, cancel := boundedReminderCall(ctx)
	defer cancel()

	if _, err := clientsFrom(ctx).Reminder.UpdateReminder(callCtx, connect.NewRequest(b.Build())); err != nil {
		log.Z.Error("failed to call UpdateReminder.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content:   fmt.Sprintf("Reminder updated for %s.%s", timestampWithRelative(fireAt), repeatNote),
		Ephemeral: true,
	}, nil
}

// callerLocation resolves the caller's timezone, falling back to UTC. loc
// resolves relative phrases; the name is stored so the server's NextOccurrence
// advances a repeat in that zone across DST. Do not stop sending it.
func callerLocation(ctx context.Context) (*time.Location, string) {
	callCtx, cancel := boundedReminderCall(ctx)
	defer cancel()

	resp, err := clientsFrom(ctx).User.GetUser(callCtx, connect.NewRequest(pb.GetUserReq_builder{}.Build()))
	if err != nil {
		log.Z.Warn("failed to resolve caller timezone, using UTC", zap.Error(err))
		return time.UTC, defaultTimezone
	}

	timezone := resp.Msg.GetUser().GetTimezone()
	if timezone == "" {
		return time.UTC, defaultTimezone
	}

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Z.Warn("caller has an unknown timezone, using UTC",
			zap.String("timezone", timezone), zap.Error(err))
		return time.UTC, defaultTimezone
	}

	return loc, timezone
}

// Uses callermeta's canonical jsonb shapes, so the server resolves the same
// instance and destination rows the interceptor would.
func currentDestination(ctx context.Context) (*pb.ReminderDestination, error) {
	origin, ok := originFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, errors.New("cannot determine where the command was sent"))
	}

	meta := callermeta.Origin{InstanceUID: origin.GuildID, DestinationUID: origin.ChannelID}
	platform := pb.Platform_PLATFORM_DISCORD

	return pb.ReminderDestination_builder{
		PlatformEnum:    &platform,
		InstanceMeta:    meta.InstanceMeta(),
		DestinationMeta: meta.DestinationMeta(),
	}.Build(), nil
}

func renderReminderLine(r *pb.Reminder) string {
	repeat := ""
	if r.GetRepeatCron() != "" {
		repeat = " (repeats)"
	}

	return fmt.Sprintf("`%d` — %s — %s%s",
		r.GetRef(),
		renderReminderTime(r),
		emptyDash(truncateLineField(r.GetMessage(), maxLineFieldRunes)),
		repeat,
	)
}

// A Discord tag renders in each viewer's own zone, not the reminder's stored
// one. That column is still used by the server and by clients without tags.
func renderReminderTime(r *pb.Reminder) string {
	return timestampWithRelative(r.GetDatetime().AsTime())
}

func reminderStatusName(s pb.ReminderStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "REMINDER_STATUS_"))
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
