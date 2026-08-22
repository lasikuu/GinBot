package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
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

// TestAccountAge covers the age shown by userinfo (AC 5). The singular "1 day"
// is a special case, and a future timestamp must not render a negative age
// oddly.
func TestAccountAge(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		createdAt time.Time
		want      string
	}{
		{name: "brand new", createdAt: now, want: "0 days"},
		{name: "under a day", createdAt: now.Add(-5 * time.Hour), want: "0 days"},
		{name: "exactly one day", createdAt: now.Add(-24 * time.Hour), want: "1 day"},
		{name: "two days", createdAt: now.Add(-48 * time.Hour), want: "2 days"},
		{name: "about a year", createdAt: now.Add(-365 * 24 * time.Hour), want: "365 days"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accountAge(tt.createdAt); got != tt.want {
				t.Errorf("accountAge() = %q, want %q", got, tt.want)
			}
		})
	}

	// A createdAt in the future yields a non-positive day count; it must not
	// render the singular "1 day" nor panic.
	if got := accountAge(now.Add(24 * time.Hour)); got == "1 day" {
		t.Errorf("a future timestamp rendered as %q", got)
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
