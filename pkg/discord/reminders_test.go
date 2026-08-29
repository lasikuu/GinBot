package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// reminderFor builds a Reminder as GetReminder returns one.
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

// TestFormatReminderInfoRendersEveryRequiredField: the /reminderinfo view must
// carry every line.
func TestFormatReminderInfoRendersEveryRequiredField(t *testing.T) {
	got := formatReminderInfo(reminderFor("chan-42"))

	for _, label := range []string{"When:", "Status:", "Message:", "Repeat:", "Destination:", "Created:", "Updated:"} {
		if !strings.Contains(got, label) {
			t.Errorf("rendering is missing the %q line:\n%s", label, got)
		}
	}
}

// TestFormatReminderInfoRendersTimestampsAsDiscordTags: fire time is absolute
// plus relative; created and updated are relative alone.
func TestFormatReminderInfoRendersTimestampsAsDiscordTags(t *testing.T) {
	r := reminderFor("chan-42")
	got := formatReminderInfo(r)

	tests := []struct {
		label string
		want  string
	}{
		{label: "when", want: timestampWithRelative(r.GetDatetime().AsTime())},
		{label: "created", want: timestampTag(r.GetCreatedAt().AsTime(), timestampRelative)},
		{label: "updated", want: timestampTag(r.GetUpdatedAt().AsTime(), timestampRelative)},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			if !strings.Contains(got, tt.want) {
				t.Errorf("%s not rendered as the Discord tag %q:\n%s", tt.label, tt.want, got)
			}
		})
	}
}

// TestFormatReminderInfoNoLongerRendersAWallClock: no instant may print a
// formatted wall clock, or a viewer would see two disagreeing times on one line.
func TestFormatReminderInfoNoLongerRendersAWallClock(t *testing.T) {
	r := reminderFor("chan-42")
	got := formatReminderInfo(r)

	loc, err := time.LoadLocation("Europe/Helsinki")
	if err != nil {
		t.Fatalf("load Europe/Helsinki: %v", err)
	}
	const layout = "2006-01-02 15:04 MST"

	// Guard the premise: a non-offset zone would make the assertion trivial.
	if stamp := r.GetDatetime().AsTime().In(loc).Format(layout); !strings.Contains(stamp, "EEST") {
		t.Fatalf("test premise drifted: August in Helsinki rendered as %q, expected EEST", stamp)
	}

	for _, instant := range []time.Time{
		r.GetDatetime().AsTime(),
		r.GetCreatedAt().AsTime(),
		r.GetUpdatedAt().AsTime(),
	} {
		for _, zone := range []*time.Location{loc, time.UTC} {
			if stamp := instant.In(zone).Format(layout); strings.Contains(got, stamp) {
				t.Errorf("a hard-coded wall clock %q is still rendered:\n%s", stamp, got)
			}
		}
	}
}

// TestRenderReminderTimeIsAbsoluteAndRelative: the fire time is absolute plus
// relative, and the list and detail views share the helper.
func TestRenderReminderTimeIsAbsoluteAndRelative(t *testing.T) {
	r := reminderFor("chan-42")
	fireAt := r.GetDatetime().AsTime()

	got := renderReminderTime(r)

	for _, want := range []string{
		timestampTag(fireAt, timestampLongDateTime),
		timestampTag(fireAt, timestampRelative),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderReminderTime() = %q, missing %q", got, want)
		}
	}

	if line := renderReminderLine(r); !strings.Contains(line, got) {
		t.Errorf("renderReminderLine() = %q, does not carry %q", line, got)
	}
}

// TestRenderReminderStampAbsence: an absent instant reads as an em dash, not the
// Unix epoch that <t:0:R> would show as "56 years ago".
func TestRenderReminderStampAbsence(t *testing.T) {
	instant := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)

	if got := renderReminderStamp(false, instant); got != "—" {
		t.Errorf("renderReminderStamp(absent) = %q, want an em dash", got)
	}
	if got, want := renderReminderStamp(true, instant), timestampTag(instant, timestampRelative); got != want {
		t.Errorf("renderReminderStamp(present) = %q, want %q", got, want)
	}
}

// TestReminderCommandsAreGroupedUnderReminder pins Group/Sub and the untouched
// flat Name that keeps legacy invocations working.
func TestReminderCommandsAreGroupedUnderReminder(t *testing.T) {
	tests := []struct {
		wantName string
		wantSub  string
		cmd      command.Command
	}{
		{wantName: "remind", wantSub: reminderSubAdd, cmd: remindCommand()},
		{wantName: "reminders", wantSub: reminderSubList, cmd: remindersCommand()},
		{wantName: "reminderdel", wantSub: reminderSubDel, cmd: reminderDelCommand()},
		{wantName: "remindermod", wantSub: reminderSubMod, cmd: reminderModCommand()},
		{wantName: "reminderinfo", wantSub: reminderSubInfo, cmd: reminderInfoCommand()},
	}

	for _, tt := range tests {
		t.Run(tt.wantName, func(t *testing.T) {
			if tt.cmd.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", tt.cmd.Name, tt.wantName)
			}
			if tt.cmd.Group != reminderGroup {
				t.Errorf("Group = %q, want %q", tt.cmd.Group, reminderGroup)
			}
			if tt.cmd.Sub != tt.wantSub {
				t.Errorf("Sub = %q, want %q", tt.cmd.Sub, tt.wantSub)
			}
		})
	}
}

// TestFormatReminderInfoRendersDestinationAsAChannelMention: shown as <#id>.
func TestFormatReminderInfoRendersDestinationAsAChannelMention(t *testing.T) {
	got := formatReminderInfo(reminderFor("chan-42"))

	if !strings.Contains(got, "Destination: <#chan-42>") {
		t.Errorf("destination not rendered as a channel mention:\n%s", got)
	}
}

// TestDestinationMentionHandlesEveryMissingShape: every missing shape renders an
// em dash, never a broken `<#>` mention and never a panic.
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

// TestFormatReminderInfoWithoutOptionalFields: a reminder missing every optional
// field still renders every line.
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

// TestRepeatOrNoneStatesTheAbsence: no repeat says "none" rather than dropping
// the line.
func TestRepeatOrNoneStatesTheAbsence(t *testing.T) {
	if got := repeatOrNone(""); got != "none" {
		t.Errorf("repeatOrNone(\"\") = %q, want %q", got, "none")
	}
	if got := repeatOrNone("@daily"); got != "`@daily`" {
		t.Errorf("repeatOrNone(@daily) = %q, want %q", got, "`@daily`")
	}
}

func findArg(args []command.Arg, name string) (command.Arg, bool) {
	for _, arg := range args {
		if arg.Name == name {
			return arg, true
		}
	}
	return command.Arg{}, false
}

// TestRemindCommandsExposeAnOptionalRepeat: both commands carry an optional
// repeat argument; required would break every existing invocation.
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

// TestRepeatArgDescriptionDocumentsTheAcceptedForms: the description must name
// the forms the server accepts.
func TestRepeatArgDescriptionDocumentsTheAcceptedForms(t *testing.T) {
	for _, form := range []string{"cron", "@daily", "@every"} {
		if !strings.Contains(repeatArgDescription, form) {
			t.Errorf("repeat description does not mention %q: %q", form, repeatArgDescription)
		}
	}
}

// TestRemindermodDocumentsTheClearSentinel: an omitted repeat leaves it alone, so
// the description must name the word that clears it.
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
