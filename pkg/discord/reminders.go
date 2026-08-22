package discord

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/reminder"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// reminderListLimit caps how many reminders the list command renders, so a user
// with many reminders gets a readable reply rather than one truncated mid-line.
// The server lists soonest-first, so this shows the reminders about to fire.
const reminderListLimit = 25

// repeatArgDescription documents the accepted repeat forms once, for both the
// create and the modify command.
const repeatArgDescription = "Repeat schedule: 5-field cron (\"0 9 * * *\"), @daily, or @every 12h"

// clearRepeatSentinel is what a user types to REMOVE an existing repeat with
// remindermod.
//
// An omitted repeat argument cannot mean "clear": /remindermod also changes the
// time and message, and treating absence as a clear is exactly the bug where
// editing a reminder's text silently turned a repeating reminder into a one-shot.
// So absence means "leave the schedule alone" and clearing needs saying out loud.
const clearRepeatSentinel = "none"

func remindCommand() command.Command {
	return command.Command{
		Name:        "remind",
		Description: "Set a reminder, e.g. remind \"in 2 hours\" \"stretch\"",
		Args: []command.Arg{
			{
				// The time phrase is a single argument. In chat it must therefore
				// be quoted when it spans words ("in 2 hours"); a single-token
				// duration ("2h30m") needs no quotes. As a slash command it is one
				// option and quoting is irrelevant.
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
				// Optional: without this argument the repeat feature had no
				// client surface at all and repeat_cron was reachable only by
				// hand-crafted gRPC.
				Name:        "repeat",
				Description: repeatArgDescription,
				Type:        command.ArgString,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
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
		return nil, status.Errorf(codes.InvalidArgument,
			"could not understand the time %q", whenInput)
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
	// There is nothing to clear at create time, so the sentinel is simply "no
	// repeat" here rather than a special case.
	if repeat != "" && !strings.EqualFold(repeat, clearRepeatSentinel) {
		b.RepeatCron = &repeat
	}

	if _, err := client.ReminderServiceClient.CreateReminder(ctx, b.Build()); err != nil {
		log.Z.Error("failed to call CreateReminder.", zap.Error(err))
		return nil, err
	}

	content := fmt.Sprintf("Reminder set for %s.", renderTime(fireAt, loc))
	if b.RepeatCron != nil {
		content += fmt.Sprintf(" Repeats: `%s`.", *b.RepeatCron)
	}

	return &command.Response{Content: content, Ephemeral: true}, nil
}

func remindersCommand() command.Command {
	return command.Command{
		Name:        "reminders",
		Description: "List your reminders",
		Clearance:   pb.Clearance_CLEARANCE_REGISTERED,
		Handler:     listReminders,
	}
}

func listReminders(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	limit := int64(reminderListLimit)
	req := pb.ListRemindersReq_builder{
		Limit: &limit,
	}.Build()

	resp, err := client.ReminderServiceClient.ListReminders(ctx, req)
	if err != nil {
		log.Z.Error("failed to call ListReminders.", zap.Error(err))
		return nil, err
	}

	reminders := resp.GetReminders()
	if len(reminders) == 0 {
		return &command.Response{Content: "You have no reminders.", Ephemeral: true}, nil
	}

	var b strings.Builder
	b.WriteString("**Your reminders**\n")
	for _, r := range reminders {
		b.WriteString(renderReminderLine(r))
		b.WriteByte('\n')
	}

	return &command.Response{Content: b.String(), Ephemeral: true}, nil
}

func reminderInfoCommand() command.Command {
	return command.Command{
		Name:        "reminderinfo",
		Description: "Show one of your reminders",
		Args: []command.Arg{
			{
				Name:        "id",
				Description: "Reminder id",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   reminderInfo,
	}
}

func reminderInfo(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	id := inv.String("id")
	req := pb.GetReminderReq_builder{Id: &id}.Build()

	resp, err := client.ReminderServiceClient.GetReminder(ctx, req)
	if err != nil {
		log.Z.Error("failed to call GetReminder.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content:   formatReminderInfo(resp.GetReminder()),
		Ephemeral: true,
	}, nil
}

// formatReminderInfo renders the full detail view of one reminder: when,
// message, repeat, created, updated and destination.
//
// It is a pure function of the protobuf so it can be unit-tested without a
// Discord session or a gRPC client. Every timestamp goes through the same
// zone-aware helper as the list view, so a reminder is shown in its own timezone
// throughout and one place decides the UTC fallback.
func formatReminderInfo(r *pb.Reminder) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Reminder** `%s`\n", r.GetId())
	fmt.Fprintf(&b, "When: %s\n", renderReminderTime(r))
	fmt.Fprintf(&b, "Status: %s\n", reminderStatusName(r.GetStatus()))
	fmt.Fprintf(&b, "Message: %s\n", emptyDash(r.GetMessage()))
	fmt.Fprintf(&b, "Repeat: %s\n", repeatOrNone(r.GetRepeatCron()))
	fmt.Fprintf(&b, "Destination: %s\n", destinationMention(r.GetDestination()))
	fmt.Fprintf(&b, "Created: %s\n", renderReminderStamp(r, r.HasCreatedAt(), r.GetCreatedAt().AsTime()))
	fmt.Fprintf(&b, "Updated: %s", renderReminderStamp(r, r.HasUpdatedAt(), r.GetUpdatedAt().AsTime()))

	return b.String()
}

// repeatOrNone renders a repeat schedule, or says there is none, so the line is
// always present and a user can see that a reminder does not repeat.
func repeatOrNone(repeatCron string) string {
	if repeatCron == "" {
		return "none"
	}

	return "`" + repeatCron + "`"
}

// destinationMention renders where a reminder will be posted as a Discord
// channel mention.
//
// The channel id lives in the destination's destination_meta under the storage
// key owned by callermeta, which is also what wrote it. A reminder with no
// resolvable channel (its destination row is gone, or it is a direct message)
// renders as an em dash rather than a broken `<#>`.
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

// renderReminderStamp renders a created/updated instant in the reminder's own
// timezone, or an em dash when the server did not send one.
func renderReminderStamp(r *pb.Reminder, present bool, instant time.Time) string {
	if !present {
		return emptyDash("")
	}

	return reminder.RenderInZone(instant, r.GetTimezone())
}

func reminderDelCommand() command.Command {
	return command.Command{
		Name:        "reminderdel",
		Description: "Delete one or more of your reminders by id",
		Args: []command.Arg{
			{
				Name:        "ids",
				Description: "Reminder ids, space-separated",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   deleteReminders,
	}
}

// deleteReminders accepts several space-separated ids. Chat tokenising splits on
// whitespace, so a slash-command user passes them as one option and a chat user
// as trailing words; the ids argument re-splits either form. The RPC deletes one
// id, so this loops.
func deleteReminders(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	ids := strings.Fields(inv.String("ids"))
	if len(ids) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "at least one reminder id is required")
	}

	var deleted, failed int
	for _, id := range ids {
		req := pb.DeleteReminderReq_builder{Id: &id}.Build()
		if _, err := client.ReminderServiceClient.DeleteReminder(ctx, req); err != nil {
			// NotFound covers "not yours" too; keep going so one bad id does not
			// abort the rest.
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
		Args: []command.Arg{
			{
				Name:        "id",
				Description: "Reminder id",
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
				Name: "repeat",
				Description: repeatArgDescription +
					". Omit to keep the current repeat, \"" + clearRepeatSentinel + "\" to remove it",
				Type: command.ArgString,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
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
		return nil, status.Errorf(codes.InvalidArgument,
			"could not understand the time %q", whenInput)
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

	// repeat_cron is patch-shaped on the server: unset leaves the stored schedule
	// alone, an explicit empty string clears it. So the argument maps to three
	// distinct outcomes, and only one of them touches the repeat.
	repeatNote := ""
	switch {
	case repeat == "":
		// Left alone. Nothing to say and nothing to send.
	case strings.EqualFold(repeat, clearRepeatSentinel):
		cleared := ""
		b.RepeatCron = &cleared
		repeatNote = " Repeat removed."
	default:
		b.RepeatCron = &repeat
		repeatNote = fmt.Sprintf(" Repeats: `%s`.", repeat)
	}

	if _, err := client.ReminderServiceClient.UpdateReminder(ctx, b.Build()); err != nil {
		log.Z.Error("failed to call UpdateReminder.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content:   fmt.Sprintf("Reminder updated for %s.%s", renderTime(fireAt, loc), repeatNote),
		Ephemeral: true,
	}, nil
}

// callerLocation resolves the caller's timezone by asking the server for their
// user row. It falls back to UTC when the user has no timezone set or the lookup
// fails, so a reminder can always be created — just interpreted in UTC.
func callerLocation(ctx context.Context) (*time.Location, string) {
	resp, err := client.UserServiceClient.GetUser(ctx, pb.GetUserReq_builder{}.Build())
	if err != nil {
		log.Z.Warn("failed to resolve caller timezone, using UTC", zap.Error(err))
		return time.UTC, defaultTimezone
	}

	timezone := resp.GetUser().GetTimezone()
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

// currentDestination builds the ReminderDestination for where the command was
// typed, using the canonical jsonb shapes from callermeta so the server resolves
// the same instance and destination rows the interceptor would.
func currentDestination(ctx context.Context) (*pb.ReminderDestination, error) {
	origin, ok := originFromContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.Internal, "cannot determine where the command was sent")
	}

	meta := callermeta.Origin{InstanceUID: origin.GuildID, DestinationUID: origin.ChannelID}
	platform := pb.Platform_PLATFORM_DISCORD

	return pb.ReminderDestination_builder{
		PlatformEnum:    &platform,
		InstanceMeta:    meta.InstanceMeta(),
		DestinationMeta: meta.DestinationMeta(),
	}.Build(), nil
}

// renderReminderLine renders one reminder for the list, in its own timezone.
func renderReminderLine(r *pb.Reminder) string {
	repeat := ""
	if r.GetRepeatCron() != "" {
		repeat = " (repeats)"
	}

	return fmt.Sprintf("`%s` — %s — %s%s",
		r.GetId(),
		renderReminderTime(r),
		emptyDash(r.GetMessage()),
		repeat,
	)
}

// renderReminderTime renders a reminder's fire time IN THE REMINDER'S OWN
// timezone (AC2), falling back to UTC when it is unset or unknown (AC3). Both
// behaviours live in reminder.RenderInZone so they cannot drift.
func renderReminderTime(r *pb.Reminder) string {
	return reminder.RenderInZone(r.GetDatetime().AsTime(), r.GetTimezone())
}

// renderTime formats a create/update confirmation in the caller's zone.
func renderTime(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

// reminderStatusName turns REMINDER_STATUS_PENDING into "pending".
func reminderStatusName(s pb.ReminderStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "REMINDER_STATUS_"))
}

// emptyDash renders an em dash for an empty message so a line never ends blank.
func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
