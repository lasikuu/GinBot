package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestUsageLine covers the shape shown for AC 1 ("a named command shows its
// usage"): required arguments in angle brackets, optional in square, name first.
func TestUsageLine(t *testing.T) {
	tests := []struct {
		name string
		cmd  command.Command
		want string
	}{
		{
			name: "no arguments",
			cmd:  command.Command{Name: "ping"},
			want: "ping",
		},
		{
			name: "one required",
			cmd: command.Command{Name: "help", Args: []command.Arg{
				{Name: "command", Required: true},
			}},
			want: "help <command>",
		},
		{
			name: "one optional",
			cmd: command.Command{Name: "help", Args: []command.Arg{
				{Name: "command", Required: false},
			}},
			want: "help [command]",
		},
		{
			name: "required then optional",
			cmd: command.Command{Name: "number", Args: []command.Arg{
				{Name: "lower", Required: true},
				{Name: "upper", Required: false},
			}},
			want: "number <lower> [upper]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usageLine(tt.cmd); got != tt.want {
				t.Errorf("usageLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDescribeCommand covers AC 1's "description, usage, arguments and aliases".
// It asserts each element is present rather than pinning the exact layout, so a
// wording tweak does not break it but a dropped element does.
func TestDescribeCommand(t *testing.T) {
	cmd := command.Command{
		Name:        "doubles",
		Description: "Roll for doubles",
		Aliases:     []string{"tuplat"},
		Args: []command.Arg{
			{Name: "count", Description: "how many", Type: command.ArgInt, Required: true},
			{Name: "loud", Description: "shout it", Type: command.ArgBool, Required: false},
		},
		Clearance: pb.Clearance_CLEARANCE_MODERATOR,
	}

	got := describeCommand(cmd)

	mustContain := []string{
		"doubles",          // name
		"Roll for doubles", // description
		"doubles <count>",  // usage, required in angle brackets
		"[loud]",           // optional in square brackets
		"count",            // argument name
		"how many",         // argument description
		"number",           // ArgInt rendered
		"true/false",       // ArgBool rendered
		"required",         // requirement label
		"optional",         // requirement label
		"tuplat",           // alias
		"moderator",        // clearance, lowercased and de-prefixed
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("describeCommand() missing %q\n---\n%s", want, got)
		}
	}
}

// TestDescribeCommandOmitsAbsentSections keeps the description terse for a bare
// command: no arguments header, no aliases line, no clearance note.
func TestDescribeCommandOmitsAbsentSections(t *testing.T) {
	got := describeCommand(command.Command{Name: "ping", Description: "pong"})

	for _, absent := range []string{"Arguments", "Aliases", "clearance"} {
		if strings.Contains(got, absent) {
			t.Errorf("describeCommand() for a bare command should omit %q\n---\n%s", absent, got)
		}
	}
}

// TestGroupedUsageLine covers help advertising the grouped form. The slash
// surface exposes ONLY /reminder add, so help that listed just the flat name
// would hide half of what the bot accepts.
func TestGroupedUsageLine(t *testing.T) {
	grouped := command.Command{
		Name: "remind", Group: "reminder", Sub: "add",
		Args: []command.Arg{
			{Name: "when", Required: true},
			{Name: "repeat"},
		},
	}

	if got, want := groupedUsageLine(grouped), "reminder add <when> [repeat]"; got != want {
		t.Errorf("groupedUsageLine() = %q, want %q", got, want)
	}
	// The flat form must still render, since chat accepts both.
	if got, want := usageLine(grouped), "remind <when> [repeat]"; got != want {
		t.Errorf("usageLine() = %q, want %q", got, want)
	}
	// An ungrouped command has no second form to advertise.
	if got := groupedUsageLine(command.Command{Name: "ping"}); got != "" {
		t.Errorf("groupedUsageLine(ungrouped) = %q, want empty", got)
	}
}

// TestDescribeCommandShowsBothForms ties the above to what a user actually sees.
func TestDescribeCommandShowsBothForms(t *testing.T) {
	got := describeCommand(command.Command{
		Name: "remind", Group: "reminder", Sub: "add",
		Description: "Set a reminder",
		Args:        []command.Arg{{Name: "when", Description: "when", Required: true}},
	})

	for _, want := range []string{"remind <when>", "reminder add <when>", "/reminder add"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeCommand() missing %q\n---\n%s", want, got)
		}
	}
}

func TestArgTypeName(t *testing.T) {
	tests := []struct {
		arg  command.ArgType
		want string
	}{
		{command.ArgString, "text"},
		{command.ArgInt, "number"},
		{command.ArgBool, "true/false"},
	}

	for _, tt := range tests {
		if got := argTypeName(tt.arg); got != tt.want {
			t.Errorf("argTypeName(%v) = %q, want %q", tt.arg, got, tt.want)
		}
	}
}

// TestClearanceName covers the label shown in help and userinfo for every enum
// value, including one outside the set so the fallback does not panic or emit
// an empty string.
func TestClearanceName(t *testing.T) {
	tests := []struct {
		clearance pb.Clearance
		want      string
	}{
		{pb.Clearance_CLEARANCE_UNSPECIFIED, "unspecified"},
		{pb.Clearance_CLEARANCE_REGISTERED, "registered"},
		{pb.Clearance_CLEARANCE_MEMBER, "member"},
		{pb.Clearance_CLEARANCE_MODERATOR, "moderator"},
		{pb.Clearance_CLEARANCE_ADMINISTRATOR, "administrator"},
		{pb.Clearance_CLEARANCE_OWNER, "owner"},
	}

	for _, tt := range tests {
		if got := clearanceName(tt.clearance); got != tt.want {
			t.Errorf("clearanceName(%v) = %q, want %q", tt.clearance, got, tt.want)
		}
	}

	// An unknown value must still render something non-empty rather than crash.
	if got := clearanceName(pb.Clearance(999)); got == "" {
		t.Error("clearanceName(unknown) returned an empty string")
	}
}

// TestFormatUserInfoRendersEveryField covers the /userinfo view (AC 5).
//
// The account line used to be a whole-day age computed in this process; it is
// now a relative Discord timestamp tag, which the viewer's own client renders.
// The assertion is still "the account's age is shown"; the expected form changed.
func TestFormatUserInfoRendersEveryField(t *testing.T) {
	createdAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	username := "kohana"
	locale := "fi"
	timezone := "Europe/Helsinki"
	clearance := pb.Clearance_CLEARANCE_MODERATOR

	user := pb.User_builder{
		Username:  &username,
		Locale:    &locale,
		Timezone:  &timezone,
		Clearance: &clearance,
		CreatedAt: timestamppb.New(createdAt),
	}.Build()

	got := formatUserInfo(user)

	for _, want := range []string{
		"kohana",
		"moderator",
		"fi",
		"Europe/Helsinki",
		timestampTag(createdAt, timestampRelative),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatUserInfo() missing %q\n---\n%s", want, got)
		}
	}
}

// TestFormatUserInfoUsesARelativeTagForTheAccountAge: the whole point of the tag
// is that the client renders it, so the rendering must not contain a wall-clock
// stamp or a day count computed here.
func TestFormatUserInfoUsesARelativeTagForTheAccountAge(t *testing.T) {
	createdAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	username := "kohana"
	user := pb.User_builder{
		Username:  &username,
		CreatedAt: timestamppb.New(createdAt),
	}.Build()

	got := formatUserInfo(user)

	if want := timestampTag(createdAt, timestampRelative); !strings.Contains(got, want) {
		t.Errorf("formatUserInfo() does not carry the relative tag %q\n---\n%s", want, got)
	}
	for _, absent := range []string{"days", "2026-02-03", "04:05"} {
		if strings.Contains(got, absent) {
			t.Errorf("formatUserInfo() still renders %q itself instead of leaving it to Discord\n---\n%s",
				absent, got)
		}
	}
}

// TestFormatUserInfoWithoutOptionalFields: an unset locale, timezone or
// created_at must read as absent. A missing created_at would otherwise render as
// <t:0:R>, which Discord shows as "56 years ago" — data, not absence.
func TestFormatUserInfoWithoutOptionalFields(t *testing.T) {
	username := "kohana"
	got := formatUserInfo(pb.User_builder{Username: &username}.Build())

	for _, want := range []string{
		"Locale: " + unsetValue,
		"Timezone: " + defaultTimezone + " (default)",
		"Account created: " + unsetValue,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatUserInfo() missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "<t:0:") {
		t.Errorf("an absent created_at rendered as the Unix epoch\n---\n%s", got)
	}
}

// TestListCommandsIncludesEveryRegistered ties AC 1's "with no name it lists the
// commands" to the actual registry: every registered command name appears.
func TestListCommandsIncludesEveryRegistered(t *testing.T) {
	// listCommands reads the package-level registry that initCommands builds.
	commandRegistry = newTestRegistry(t)

	got := listCommands()

	for _, cmd := range commandRegistry.All() {
		if !strings.Contains(got, cmd.Name) {
			t.Errorf("listCommands() omits %q\n---\n%s", cmd.Name, got)
		}
	}
}

func TestFormatLatency(t *testing.T) {
	// Rounded to the millisecond so the number shown is stable and legible.
	if got := formatLatency(1500 * time.Microsecond); got != "2ms" {
		t.Errorf("formatLatency(1.5ms) = %q, want %q", got, "2ms")
	}
}
