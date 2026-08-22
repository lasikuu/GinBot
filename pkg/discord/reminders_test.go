package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// reminderFor builds a Reminder as GetReminder returns one: the row's fields
// plus the destination the server attaches by joining back to its instance.
func reminderFor(destinationUID string) *pb.Reminder {
	id := "0192f000-0000-7000-8000-000000000001"
	timezone := "Europe/Helsinki"
	message := "stand up and stretch"
	repeat := "0 9 * * *"
	statusValue := pb.ReminderStatus_REMINDER_STATUS_PENDING
	platform := pb.Platform_PLATFORM_DISCORD

	origin := callermeta.Origin{InstanceUID: "guild-1", DestinationUID: destinationUID}

	return pb.Reminder_builder{
		Id:         &id,
		Datetime:   timestamppb.New(time.Date(2026, 8, 22, 15, 0, 0, 0, time.UTC)),
		Timezone:   &timezone,
		RepeatCron: &repeat,
		Status:     &statusValue,
		Message:    &message,
		CreatedAt:  timestamppb.New(time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)),
		UpdatedAt:  timestamppb.New(time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC)),
		Destination: pb.ReminderDestination_builder{
			PlatformEnum:    &platform,
			InstanceMeta:    origin.InstanceMeta(),
			DestinationMeta: origin.DestinationMeta(),
		}.Build(),
	}.Build()
}

// TestFormatReminderInfoRendersEveryRequiredField is the /reminderinfo contract:
// when, message, repeat, created, updated and destination must all be present.
// The view used to omit created, updated and destination even though all three
// were already on the wire.
func TestFormatReminderInfoRendersEveryRequiredField(t *testing.T) {
	got := formatReminderInfo(reminderFor("chan-42"))

	for _, label := range []string{"When:", "Status:", "Message:", "Repeat:", "Destination:", "Created:", "Updated:"} {
		if !strings.Contains(got, label) {
			t.Errorf("rendering is missing the %q line:\n%s", label, got)
		}
	}
}

// TestFormatReminderInfoRendersTimestampsInTheReminderZone: every instant goes
// through the same zone-aware helper as the list view, so a reminder reads
// consistently in its own timezone rather than mixing UTC and local.
//
// Europe/Helsinki is UTC+3 in August, so 15:00 UTC is 18:00 EEST.
func TestFormatReminderInfoRendersTimestampsInTheReminderZone(t *testing.T) {
	r := reminderFor("chan-42")
	got := formatReminderInfo(r)

	// Computed independently, not copied from the implementation.
	loc, err := time.LoadLocation("Europe/Helsinki")
	if err != nil {
		t.Fatalf("load Europe/Helsinki: %v", err)
	}
	const layout = "2006-01-02 15:04 MST"

	tests := []struct {
		label   string
		instant time.Time
	}{
		{label: "when", instant: r.GetDatetime().AsTime()},
		{label: "created", instant: r.GetCreatedAt().AsTime()},
		{label: "updated", instant: r.GetUpdatedAt().AsTime()},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			want := tt.instant.In(loc).Format(layout)
			if !strings.Contains(got, want) {
				t.Errorf("%s not rendered as %q in the reminder's zone:\n%s", tt.label, want, got)
			}
		})
	}

	// Guard the premise: if this stopped being an offset zone the test would
	// pass vacuously against a UTC render.
	if want := r.GetDatetime().AsTime().In(loc).Format(layout); !strings.Contains(want, "EEST") {
		t.Fatalf("test premise drifted: August in Helsinki rendered as %q, expected EEST", want)
	}
}

// TestFormatReminderInfoRendersDestinationAsAChannelMention: the destination is
// shown as <#id> so it is a clickable channel rather than an opaque snowflake.
func TestFormatReminderInfoRendersDestinationAsAChannelMention(t *testing.T) {
	got := formatReminderInfo(reminderFor("chan-42"))

	if !strings.Contains(got, "Destination: <#chan-42>") {
		t.Errorf("destination not rendered as a channel mention:\n%s", got)
	}
}

// TestDestinationMentionHandlesEveryMissingShape: the destination arrives as
// untyped jsonb and may be absent entirely (its row was removed, or the reminder
// is a direct message). Every such case must render an em dash, never a broken
// `<#>` mention and never a panic.
func TestDestinationMentionHandlesEveryMissingShape(t *testing.T) {
	platform := pb.Platform_PLATFORM_DISCORD
	withMeta := func(uid string) *pb.ReminderDestination {
		origin := callermeta.Origin{InstanceUID: "guild-1", DestinationUID: uid}
		return pb.ReminderDestination_builder{
			PlatformEnum:    &platform,
			InstanceMeta:    origin.InstanceMeta(),
			DestinationMeta: origin.DestinationMeta(),
		}.Build()
	}

	tests := []struct {
		name        string
		destination *pb.ReminderDestination
		want        string
	}{
		{name: "nil destination", destination: nil, want: "—"},
		{
			name:        "no destination meta",
			destination: pb.ReminderDestination_builder{PlatformEnum: &platform}.Build(),
			want:        "—",
		},
		{name: "empty destination uid", destination: withMeta(""), want: "—"},
		{name: "present", destination: withMeta("chan-7"), want: "<#chan-7>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := destinationMention(tt.destination); got != tt.want {
				t.Errorf("destinationMention = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatReminderInfoWithoutOptionalFields: a one-shot reminder with no
// message, no destination and no timestamps still renders every line, because a
// missing line reads as a rendering bug rather than as absent data.
func TestFormatReminderInfoWithoutOptionalFields(t *testing.T) {
	id := "0192f000-0000-7000-8000-000000000002"
	statusValue := pb.ReminderStatus_REMINDER_STATUS_DELIVERED

	r := pb.Reminder_builder{
		Id:       &id,
		Datetime: timestamppb.New(time.Date(2026, 8, 22, 15, 0, 0, 0, time.UTC)),
		Status:   &statusValue,
	}.Build()

	got := formatReminderInfo(r)

	for _, want := range []string{
		"Status: delivered",
		"Message: —",
		"Repeat: none",
		"Destination: —",
		"Created: —",
		"Updated: —",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRepeatOrNoneStatesTheAbsence: a reminder that does not repeat says so
// rather than dropping the line, so a user can tell "no repeat" from "the view
// forgot to tell me".
func TestRepeatOrNoneStatesTheAbsence(t *testing.T) {
	if got := repeatOrNone(""); got != "none" {
		t.Errorf("repeatOrNone(\"\") = %q, want %q", got, "none")
	}
	if got := repeatOrNone("@daily"); got != "`@daily`" {
		t.Errorf("repeatOrNone(@daily) = %q, want %q", got, "`@daily`")
	}
}

// findArg returns the named argument of a command, or false.
func findArg(args []command.Arg, name string) (command.Arg, bool) {
	for _, arg := range args {
		if arg.Name == name {
			return arg, true
		}
	}
	return command.Arg{}, false
}

// TestRemindCommandsExposeAnOptionalRepeat is the client-surface half of the
// repeat feature.
//
// Without a repeat argument on BOTH commands, repeat_cron was reachable only by
// hand-crafted gRPC: a repeat could not be created, and /remindermod could not
// update one (it silently cleared it instead). Optional, because making it
// required would break every existing invocation.
func TestRemindCommandsExposeAnOptionalRepeat(t *testing.T) {
	tests := []struct {
		name string
		args []command.Arg
	}{
		{name: "remind", args: remindCommand().Args},
		{name: "remindermod", args: reminderModCommand().Args},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arg, ok := findArg(tt.args, "repeat")
			if !ok {
				t.Fatalf("%s has no repeat argument", tt.name)
			}
			if arg.Required {
				t.Error("the repeat argument is required; it must be optional")
			}
			if arg.Type != command.ArgString {
				t.Errorf("repeat argument type = %v, want ArgString", arg.Type)
			}
		})
	}
}

// TestRepeatArgDescriptionDocumentsTheAcceptedForms: the description is the only
// place a user learns what to type, so it has to name the forms the server
// actually accepts.
func TestRepeatArgDescriptionDocumentsTheAcceptedForms(t *testing.T) {
	for _, form := range []string{"cron", "@daily", "@every"} {
		if !strings.Contains(repeatArgDescription, form) {
			t.Errorf("repeat description does not mention %q: %q", form, repeatArgDescription)
		}
	}
}

// TestRemindermodDocumentsTheClearSentinel: clearing a repeat has to be said out
// loud (an omitted argument means "leave it alone"), so the command must tell the
// user which word does it.
func TestRemindermodDocumentsTheClearSentinel(t *testing.T) {
	arg, ok := findArg(reminderModCommand().Args, "repeat")
	if !ok {
		t.Fatal("remindermod has no repeat argument")
	}
	if !strings.Contains(arg.Description, clearRepeatSentinel) {
		t.Errorf("remindermod's repeat description does not document the %q sentinel: %q",
			clearRepeatSentinel, arg.Description)
	}
}
