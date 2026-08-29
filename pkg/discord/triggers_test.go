package discord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/trigger"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	triggerID     = "0192f000-0000-7000-8000-000000000001"
	triggerFileID = "0192f000-0000-7000-8000-0000000000ff"
)

// stored is the raw chance column, which doubles as "unset".
func triggerFor(stored int32, mode pb.TriggerMode, reply string, file *pb.TriggerFile) *pb.Trigger {
	id := triggerID
	phrase := "gm"
	userID := "0192f000-0000-7000-8000-0000000000aa"

	b := pb.Trigger_builder{
		Id:        &id,
		Phrase:    &phrase,
		UserId:    &userID,
		Chance:    &stored,
		Mode:      &mode,
		File:      file,
		CreatedAt: timestamppb.New(time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)),
		UpdatedAt: timestamppb.New(time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC)),
	}
	// chk_reply_or_file is a non-exclusive OR, so both at once is a valid row.
	if reply != "" {
		b.Reply = &reply
	}

	return b.Build()
}

func triggerFileFor(fileID string, filename string, mimeType string) *pb.TriggerFile {
	byteSize := int64(16)

	return pb.TriggerFile_builder{
		FileId:   &fileID,
		Filename: &filename,
		MimeType: &mimeType,
		ByteSize: &byteSize,
	}.Build()
}

func statFor(id string, phrase string, count int64) *pb.TriggerStat {
	mode := pb.TriggerMode_TRIGGER_MODE_ANY
	chance := int32(0)

	return pb.TriggerStat_builder{
		TriggerId: &id,
		Phrase:    &phrase,
		Count:     &count,
		Chance:    &chance,
		Mode:      &mode,
	}.Build()
}

// The context is bare, so every case must be refused by argument validation
// alone; reaching the RPC nil-panics on clientsFrom(ctx).
func invokeTrigger(t *testing.T, cmd command.Command, args map[string]any) error {
	t.Helper()

	inv, err := command.BindNamed(cmd, args)
	if err != nil {
		t.Fatalf("bind %s: %v", cmd.Name, err)
	}

	_, handlerErr := cmd.Handler(context.Background(), inv)

	return handlerErr
}

func asConnectError(t *testing.T, err error) *connect.Error {
	t.Helper()

	var connErr *connect.Error
	if !errors.As(err, &connErr) {
		t.Fatalf("error %v is not a *connect.Error, so errorMessage cannot map it", err)
	}

	return connErr
}

// errorMessage passes only InvalidArgument and FailedPrecondition through
// verbatim; any other code reaches the user as "Something went wrong."
func requireInvalidArgument(t *testing.T, err error, mustMention ...string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}

	connErr := asConnectError(t, err)
	if connErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("code = %s, want %s (message %q)", connErr.Code(), connect.CodeInvalidArgument, connErr.Message())
	}

	for _, word := range mustMention {
		if !strings.Contains(strings.ToLower(connErr.Message()), strings.ToLower(word)) {
			t.Errorf("message %q does not mention %q", connErr.Message(), word)
		}
	}
}

// An unknown chat command is ignored without a reply, so a dropped alias fails
// silently.
func TestTriggerCommandsAreGroupedUnderTrigger(t *testing.T) {
	tests := []struct {
		wantName    string
		wantSub     string
		wantAliases []string
		cmd         command.Command
	}{
		{wantName: "triggeradd", wantSub: triggerSubAdd, wantAliases: []string{"triggercreate"}, cmd: triggerAddCommand()},
		{wantName: "triggerdel", wantSub: triggerSubDel, wantAliases: []string{"triggerremove"}, cmd: triggerDelCommand()},
		{wantName: "triggermod", wantSub: triggerSubMod, wantAliases: []string{"triggeredit"}, cmd: triggerModCommand()},
		{wantName: "triggers", wantSub: triggerSubList, wantAliases: []string{"triggerlist"}, cmd: triggerListCommand()},
		{wantName: "triggerinfo", wantSub: triggerSubInfo, cmd: triggerInfoCommand()},
		{wantName: "triggerstats", wantSub: triggerSubStats, cmd: triggerStatsCommand()},
		{wantName: "triggerexec", wantSub: triggerSubExec, wantAliases: []string{"triggerbyid"}, cmd: triggerExecCommand()},
	}

	for _, tt := range tests {
		t.Run(tt.wantName, func(t *testing.T) {
			if tt.cmd.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", tt.cmd.Name, tt.wantName)
			}
			if tt.cmd.Group != triggerGroup {
				t.Errorf("Group = %q, want %q", tt.cmd.Group, triggerGroup)
			}
			if tt.cmd.Sub != tt.wantSub {
				t.Errorf("Sub = %q, want %q", tt.cmd.Sub, tt.wantSub)
			}
			if !slices.Equal(tt.cmd.Aliases, tt.wantAliases) {
				t.Errorf("Aliases = %q, want %q", tt.cmd.Aliases, tt.wantAliases)
			}
			if tt.cmd.Clearance != pb.Clearance_CLEARANCE_REGISTERED {
				t.Errorf("Clearance = %v, want %v", tt.cmd.Clearance, pb.Clearance_CLEARANCE_REGISTERED)
			}
			if tt.cmd.Handler == nil {
				t.Error("Handler is nil; Registry.Register would refuse the command at boot")
			}
			if tt.cmd.Description == "" {
				t.Error("Description is empty; Discord rejects that at boot")
			}
		})
	}
}

// Order matters: chat arguments bind positionally, and Registry.Register
// log.Z.Fatals at boot on a required argument declared after an optional one.
func TestTriggerCommandArguments(t *testing.T) {
	type wantArg struct {
		name     string
		argType  command.ArgType
		required bool
	}

	tests := []struct {
		cmd  command.Command
		want []wantArg
	}{
		{
			cmd: triggerAddCommand(),
			want: []wantArg{
				{name: "phrase", argType: command.ArgString, required: true},
				{name: "reply", argType: command.ArgString},
				{name: "file", argType: command.ArgString},
				{name: "chance", argType: command.ArgInt},
				{name: "mode", argType: command.ArgString},
			},
		},
		{
			cmd: triggerDelCommand(),
			want: []wantArg{
				{name: "id", argType: command.ArgString, required: true},
			},
		},
		{
			cmd: triggerModCommand(),
			want: []wantArg{
				{name: "id", argType: command.ArgString, required: true},
				{name: "phrase", argType: command.ArgString},
				{name: "reply", argType: command.ArgString},
				{name: "file", argType: command.ArgString},
				{name: "chance", argType: command.ArgInt},
				{name: "mode", argType: command.ArgString},
			},
		},
		{
			cmd: triggerListCommand(),
			want: []wantArg{
				{name: "search", argType: command.ArgString},
				{name: "mine", argType: command.ArgBool},
				{name: "limit", argType: command.ArgInt},
			},
		},
		{
			cmd: triggerInfoCommand(),
			want: []wantArg{
				{name: "id", argType: command.ArgString, required: true},
			},
		},
		{
			cmd: triggerStatsCommand(),
			want: []wantArg{
				{name: "kind", argType: command.ArgString},
				{name: "limit", argType: command.ArgInt},
			},
		},
		{
			cmd: triggerExecCommand(),
			want: []wantArg{
				{name: "id", argType: command.ArgString, required: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.cmd.Name, func(t *testing.T) {
			gotNames := make([]string, 0, len(tt.cmd.Args))
			for _, arg := range tt.cmd.Args {
				gotNames = append(gotNames, arg.Name)
			}
			wantNames := make([]string, 0, len(tt.want))
			for _, arg := range tt.want {
				wantNames = append(wantNames, arg.name)
			}
			if !slices.Equal(gotNames, wantNames) {
				t.Fatalf("arguments = %q, want %q", gotNames, wantNames)
			}

			for i, want := range tt.want {
				arg := tt.cmd.Args[i]
				if arg.Type != want.argType {
					t.Errorf("%q type = %v, want %v", arg.Name, arg.Type, want.argType)
				}
				if arg.Required != want.required {
					t.Errorf("%q required = %v, want %v", arg.Name, arg.Required, want.required)
				}
				// Discord rejects an empty description at boot.
				if arg.Description == "" {
					t.Errorf("%q has no description", arg.Name)
				}
			}
		})
	}
}

func TestTriggerAliasesResolve(t *testing.T) {
	registry := newTestRegistry(t)

	tests := []struct {
		alias string
		want  string
	}{
		{alias: "triggercreate", want: "triggeradd"},
		{alias: "TRIGGERCREATE", want: "triggeradd"},
		{alias: "triggerremove", want: "triggerdel"},
		{alias: "triggeredit", want: "triggermod"},
		{alias: "triggerlist", want: "triggers"},
		{alias: "triggerbyid", want: "triggerexec"},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			cmd, ok := registry.Lookup(tt.alias)
			if !ok {
				t.Fatalf("alias %q does not resolve", tt.alias)
			}
			if cmd.Name != tt.want {
				t.Errorf("alias %q resolved to %q, want %q", tt.alias, cmd.Name, tt.want)
			}
		})
	}
}

func TestTriggerCommandsGenerateOneGroupedCommand(t *testing.T) {
	registry := newTestRegistry(t)
	byName := generatedByName(t, registry)

	parent, ok := byName[triggerGroup]
	if !ok {
		t.Fatalf("no %q command was generated", triggerGroup)
	}
	if parent.Description == "" {
		t.Error("the trigger group parent has no description; Discord rejects that")
	}

	wantSubs := []string{
		triggerSubAdd, triggerSubDel, triggerSubMod, triggerSubList,
		triggerSubInfo, triggerSubStats, triggerSubExec,
	}

	gotSubs := make([]string, 0, len(parent.Options))
	for _, option := range parent.Options {
		gotSubs = append(gotSubs, option.Name)
		// Discord rejects a command mixing subcommands with ordinary options.
		if option.Type != discordgo.ApplicationCommandOptionSubCommand {
			t.Errorf("%q option %q has type %v, want SubCommand", triggerGroup, option.Name, option.Type)
		}
	}

	slices.Sort(gotSubs)
	slices.Sort(wantSubs)
	if !slices.Equal(gotSubs, wantSubs) {
		t.Errorf("%q subcommands = %q, want %q", triggerGroup, gotSubs, wantSubs)
	}

	for _, name := range []string{
		"triggeradd", "triggerdel", "triggermod", "triggers",
		"triggerinfo", "triggerstats", "triggerexec",
	} {
		if _, top := byName[name]; top {
			t.Errorf("%q is also generated as a top-level command", name)
		}
	}
}

// A slash invocation delivers arguments as the subcommand's own options.
func TestTriggerSubCommandsCarryTheirOwnArguments(t *testing.T) {
	byName := generatedByName(t, newTestRegistry(t))

	parent, ok := byName[triggerGroup]
	if !ok {
		t.Fatalf("no %q command was generated", triggerGroup)
	}

	tests := []struct {
		sub string
		cmd command.Command
	}{
		{sub: triggerSubAdd, cmd: triggerAddCommand()},
		{sub: triggerSubDel, cmd: triggerDelCommand()},
		{sub: triggerSubMod, cmd: triggerModCommand()},
		{sub: triggerSubList, cmd: triggerListCommand()},
		{sub: triggerSubInfo, cmd: triggerInfoCommand()},
		{sub: triggerSubStats, cmd: triggerStatsCommand()},
		{sub: triggerSubExec, cmd: triggerExecCommand()},
	}

	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			sub, found := subCommandOf(parent, tt.sub)
			if !found {
				t.Fatalf("%q has no %q subcommand", triggerGroup, tt.sub)
			}
			if len(sub.Options) != len(tt.cmd.Args) {
				t.Fatalf("%q %q has %d options, want %d",
					triggerGroup, tt.sub, len(sub.Options), len(tt.cmd.Args))
			}

			for i, arg := range tt.cmd.Args {
				option := sub.Options[i]
				if option.Name != arg.Name {
					t.Errorf("option %d = %q, want %q", i, option.Name, arg.Name)
				}
				if option.Type != applicationCommandOptionType(arg.Type) {
					t.Errorf("%q type = %v, want %v", arg.Name, option.Type,
						applicationCommandOptionType(arg.Type))
				}
				if option.Required != arg.Required {
					t.Errorf("%q required = %v, want %v", arg.Name, option.Required, arg.Required)
				}
			}
		})
	}
}

func TestChatResolvesEveryTriggerSubcommand(t *testing.T) {
	registry := newTestRegistry(t)

	tests := []struct {
		sub  string
		want string
	}{
		{sub: triggerSubAdd, want: "triggeradd"},
		{sub: triggerSubDel, want: "triggerdel"},
		{sub: triggerSubMod, want: "triggermod"},
		{sub: triggerSubList, want: "triggers"},
		{sub: triggerSubInfo, want: "triggerinfo"},
		{sub: triggerSubStats, want: "triggerstats"},
		{sub: triggerSubExec, want: "triggerexec"},
	}

	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			cmd, rest, ok := registry.ResolveChat(triggerGroup, []string{tt.sub, "gm", "good morning"})
			if !ok {
				t.Fatalf("??%s %s does not resolve", triggerGroup, tt.sub)
			}
			if cmd.Name != tt.want {
				t.Errorf("??%s %s resolved to %q, want %q", triggerGroup, tt.sub, cmd.Name, tt.want)
			}
			if !slices.Equal(rest, []string{"gm", "good morning"}) {
				t.Errorf("remaining arguments = %q, want the two after the subcommand", rest)
			}
		})
	}
}

// An unsupplied optional string argument is indistinguishable from an empty
// one, so "" must mean "not specified" rather than be refused.
func TestParseTriggerMode(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		want   pb.TriggerMode
		wantOK bool
	}{
		{name: "unspecified", value: "", want: pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED, wantOK: true},
		{name: "whitespace is unspecified", value: "   ", want: pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED, wantOK: true},
		{name: "exact", value: "exact", want: pb.TriggerMode_TRIGGER_MODE_EXACT, wantOK: true},
		{name: "any", value: "any", want: pb.TriggerMode_TRIGGER_MODE_ANY, wantOK: true},
		{name: "regex", value: "regex", want: pb.TriggerMode_TRIGGER_MODE_REGEX, wantOK: true},
		{name: "upper case", value: "EXACT", want: pb.TriggerMode_TRIGGER_MODE_EXACT, wantOK: true},
		{name: "mixed case", value: "Regex", want: pb.TriggerMode_TRIGGER_MODE_REGEX, wantOK: true},
		{name: "surrounding space", value: " any ", want: pb.TriggerMode_TRIGGER_MODE_ANY, wantOK: true},
		{name: "nonsense", value: "sometimes", wantOK: false},
		{
			name:   "the wire enum name is not a mode word",
			value:  "TRIGGER_MODE_EXACT",
			wantOK: false,
		},
		{name: "unspecified is not typeable", value: "unspecified", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTriggerMode(tt.value)
			if ok != tt.wantOK {
				t.Fatalf("parseTriggerMode(%q) ok = %v, want %v", tt.value, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("parseTriggerMode(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestTriggerModeNamesListsEveryAcceptedMode(t *testing.T) {
	got := triggerModeNames()

	for _, mode := range []string{"exact", "any", "regex"} {
		if !strings.Contains(got, mode) {
			t.Errorf("triggerModeNames() = %q, does not mention %q", got, mode)
		}
		if _, ok := parseTriggerMode(mode); !ok {
			t.Errorf("%q is advertised but parseTriggerMode refuses it", mode)
		}
	}
}

func TestParseStatsKind(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		want   pb.ActionType
		wantOK bool
	}{
		{name: "default", value: "", want: pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED, wantOK: true},
		{name: "whitespace defaults too", value: "  ", want: pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED, wantOK: true},
		{name: "occurred", value: "occurred", want: pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED, wantOK: true},
		{name: "called", value: "called", want: pb.ActionType_ACTION_TYPE_TRIGGER_CALLED, wantOK: true},
		{name: "upper case", value: "CALLED", want: pb.ActionType_ACTION_TYPE_TRIGGER_CALLED, wantOK: true},
		{name: "surrounding space", value: " occurred ", want: pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED, wantOK: true},
		{name: "nonsense", value: "everything", wantOK: false},
		{
			name:   "another action type is not a kind word",
			value:  "reminder_created",
			wantOK: false,
		},
		{name: "the wire enum name is not a kind word", value: "ACTION_TYPE_TRIGGER_CALLED", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseStatsKind(tt.value)
			if ok != tt.wantOK {
				t.Fatalf("parseStatsKind(%q) ok = %v, want %v", tt.value, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("parseStatsKind(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestStatsKindNamesListsEveryAcceptedKind(t *testing.T) {
	got := statsKindNames()

	for _, kind := range []string{"occurred", "called"} {
		if !strings.Contains(got, kind) {
			t.Errorf("statsKindNames() = %q, does not mention %q", got, kind)
		}
		if _, ok := parseStatsKind(kind); !ok {
			t.Errorf("%q is advertised but parseStatsKind refuses it", kind)
		}
	}
}

// See ADR-0021: a stored 0 means the default, and exact mode weights on top.
func TestTriggerRenderingShowsTheEffectiveChance(t *testing.T) {
	tests := []struct {
		name     string
		stored   int32
		mode     pb.TriggerMode
		want     string
		unwanted string
	}{
		{
			name:     "a stored zero reads as the default, not as never",
			stored:   0,
			mode:     pb.TriggerMode_TRIGGER_MODE_ANY,
			want:     "5%",
			unwanted: "0%",
		},
		{
			name:     "an unspecified mode is weighted as any",
			stored:   0,
			mode:     pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED,
			want:     "5%",
			unwanted: "0%",
		},
		{
			name:     "exact mode triples the default",
			stored:   0,
			mode:     pb.TriggerMode_TRIGGER_MODE_EXACT,
			want:     "15%",
			unwanted: "0%",
		},
		{
			name:     "exact mode clamps instead of exceeding certainty",
			stored:   40,
			mode:     pb.TriggerMode_TRIGGER_MODE_EXACT,
			want:     "100%",
			unwanted: "40%",
		},
		{
			name:   "an explicit any-mode chance is shown as it is",
			stored: 40,
			mode:   pb.TriggerMode_TRIGGER_MODE_ANY,
			want:   "40%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Guards against pinning a stale number if the weighting changes.
			if premise := fmt.Sprintf("%d%%", trigger.EffectiveChance(tt.stored, tt.mode)); premise != tt.want {
				t.Fatalf("test premise drifted: EffectiveChance(%d, %v) renders as %q, want %q",
					tt.stored, tt.mode, premise, tt.want)
			}

			trg := triggerFor(tt.stored, tt.mode, "good morning", nil)

			for _, rendering := range []struct {
				name string
				got  string
			}{
				{name: "formatTriggerInfo", got: formatTriggerInfo(trg)},
				{name: "renderTriggerLine", got: renderTriggerLine(trg)},
			} {
				if !strings.Contains(rendering.got, tt.want) {
					t.Errorf("%s does not show the effective chance %q:\n%s",
						rendering.name, tt.want, rendering.got)
				}
				if tt.unwanted != "" && strings.Contains(rendering.got, tt.unwanted) {
					t.Errorf("%s shows the STORED chance %q, which is not what the trigger does:\n%s",
						rendering.name, tt.unwanted, rendering.got)
				}
			}
		})
	}
}

func TestTriggerRenderingCarriesTheIdentifyingFields(t *testing.T) {
	trg := triggerFor(10, pb.TriggerMode_TRIGGER_MODE_ANY, "good morning", nil)

	for _, rendering := range []struct {
		name string
		got  string
	}{
		{name: "formatTriggerInfo", got: formatTriggerInfo(trg)},
		{name: "renderTriggerLine", got: renderTriggerLine(trg)},
	} {
		if !strings.Contains(rendering.got, trg.GetId()) {
			t.Errorf("%s does not show the id %q:\n%s", rendering.name, trg.GetId(), rendering.got)
		}
		if !strings.Contains(rendering.got, trg.GetPhrase()) {
			t.Errorf("%s does not show the phrase %q:\n%s", rendering.name, trg.GetPhrase(), rendering.got)
		}
	}
}

func TestFormatTriggerInfoShowsTheModeAndWhetherThereIsAFile(t *testing.T) {
	for _, mode := range []pb.TriggerMode{
		pb.TriggerMode_TRIGGER_MODE_EXACT,
		pb.TriggerMode_TRIGGER_MODE_ANY,
		pb.TriggerMode_TRIGGER_MODE_REGEX,
	} {
		word := strings.ToLower(strings.TrimPrefix(mode.String(), "TRIGGER_MODE_"))
		t.Run(word, func(t *testing.T) {
			got := formatTriggerInfo(triggerFor(10, mode, "good morning", nil))
			if !strings.Contains(strings.ToLower(got), word) {
				t.Errorf("the rendering does not name the mode %q:\n%s", word, got)
			}
		})
	}

	withFile := formatTriggerInfo(triggerFor(10, pb.TriggerMode_TRIGGER_MODE_ANY, "",
		triggerFileFor(triggerFileID, "cat.png", "image/png")))
	withoutFile := formatTriggerInfo(triggerFor(10, pb.TriggerMode_TRIGGER_MODE_ANY, "", nil))

	if withFile == withoutFile {
		t.Errorf("a trigger with a file renders identically to one without:\n%s", withFile)
	}
}

// discordgo dispatches handlers without a recover(), so a nil deref here would
// take the process down. The assertion is that these return at all.
func TestTriggerRenderingIsNilSafe(t *testing.T) {
	formatTriggerInfo(nil)
	renderTriggerLine(nil)
	formatTriggerStats(nil, pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED)
	formatTriggerStats([]*pb.TriggerStat{nil}, pb.ActionType_ACTION_TYPE_TRIGGER_CALLED)
}

// The wording is not pinned, only that the two boards do not render identically.
func TestFormatTriggerStatsNamesTheKindAndRendersEveryRow(t *testing.T) {
	stats := []*pb.TriggerStat{
		statFor(triggerID, "gm", 41),
		statFor("0192f000-0000-7000-8000-000000000002", "gn", 27),
	}

	occurred := formatTriggerStats(stats, pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED)
	called := formatTriggerStats(stats, pb.ActionType_ACTION_TYPE_TRIGGER_CALLED)

	if occurred == called {
		t.Errorf("both leaderboards render identically, so neither says which was asked for:\n%s", occurred)
	}

	tests := []struct {
		name string
		got  string
	}{
		{name: "occurred", got: occurred},
		{name: "called", got: called},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.TrimSpace(tt.got) == "" {
				t.Fatal("the leaderboard rendered nothing")
			}

			lines := strings.Split(tt.got, "\n")
			for _, stat := range stats {
				matched := 0
				for _, line := range lines {
					if strings.Contains(line, stat.GetPhrase()) {
						matched++
					}
				}
				if matched != 1 {
					t.Errorf("phrase %q appears on %d lines, want 1:\n%s", stat.GetPhrase(), matched, tt.got)
				}
				if count := fmt.Sprint(stat.GetCount()); !strings.Contains(tt.got, count) {
					t.Errorf("the count %s for %q is not rendered:\n%s", count, stat.GetPhrase(), tt.got)
				}
			}
		})
	}
}

func TestFormatTriggerStatsSaysWhenNothingIsRecorded(t *testing.T) {
	for _, empty := range []struct {
		name  string
		stats []*pb.TriggerStat
	}{
		{name: "nil slice", stats: nil},
		{name: "empty slice", stats: []*pb.TriggerStat{}},
	} {
		t.Run(empty.name, func(t *testing.T) {
			got := formatTriggerStats(empty.stats, pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED)
			if strings.TrimSpace(got) == "" {
				t.Fatal("an empty leaderboard rendered nothing at all")
			}

			lower := strings.ToLower(got)
			if !strings.Contains(lower, "no") && !strings.Contains(lower, "nothing") {
				t.Errorf("an empty leaderboard does not say it is empty: %q", got)
			}

			if other := formatTriggerStats(empty.stats, pb.ActionType_ACTION_TYPE_TRIGGER_CALLED); got == other {
				t.Errorf("both empty leaderboards read identically: %q", got)
			}
		})
	}
}

// Every name is derived, and none may be empty: Discord rejects a nameless
// attachment.
func TestTriggerFileResponseNamesAndCarriesTheAttachment(t *testing.T) {
	content := []byte("\x89PNG not really")

	tests := []struct {
		name     string
		file     *pb.TriggerFile
		wantName string
	}{
		{
			name:     "the filename is used",
			file:     triggerFileFor(triggerFileID, "cat.png", "image/png"),
			wantName: "cat.png",
		},
		{
			name:     "no filename falls back to the file id",
			file:     triggerFileFor(triggerFileID, "", "image/png"),
			wantName: triggerFileID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := triggerFileResponse(tt.file, content)
			if resp == nil {
				t.Fatal("triggerFileResponse returned nil; the trigger would fire as nothing")
			}
			if resp.File == nil {
				t.Fatal("the response carries no file; the trigger would fire as an empty message")
			}
			if resp.File.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", resp.File.Name, tt.wantName)
			}
			if resp.File.MIMEType != "image/png" {
				t.Errorf("MIMEType = %q, want %q", resp.File.MIMEType, "image/png")
			}
			if !bytes.Equal(resp.File.Content, content) {
				t.Errorf("Content = %q, want %q", resp.File.Content, content)
			}

			files := responseFiles(resp)
			if len(files) != 1 {
				t.Fatalf("responseFiles produced %d attachments, want 1", len(files))
			}
			if files[0].Name != tt.wantName {
				t.Errorf("attachment Name = %q, want %q", files[0].Name, tt.wantName)
			}
			if files[0].ContentType != "image/png" {
				t.Errorf("attachment ContentType = %q, want %q", files[0].ContentType, "image/png")
			}
		})
	}
}

// Which constant it lands on is not pinned; that it is never blank is.
func TestTriggerFileResponseAlwaysNamesTheAttachment(t *testing.T) {
	resp := triggerFileResponse(triggerFileFor("", "", "image/png"), []byte("some bytes"))
	if resp == nil || resp.File == nil {
		t.Fatal("a file with no name and no id produced no attachment at all")
	}
	if resp.File.Name == "" {
		t.Error("the attachment has no name; Discord refuses the send and the trigger fires as nothing")
	}

	files := responseFiles(resp)
	if len(files) != 1 {
		t.Fatalf("responseFiles produced %d attachments, want 1", len(files))
	}
	if files[0].Name == "" {
		t.Error("the rendered attachment has no name")
	}
}

func TestTriggerFileResponseIsNilSafe(t *testing.T) {
	if files := responseFiles(triggerFileResponse(nil, nil)); len(files) != 0 {
		t.Errorf("a nil file produced %d attachments, want none", len(files))
	}
}

// Compared against callermeta's own builder: instance rows are looked up by
// jsonb equality, so a differently keyed struct silently duplicates the guild.
func TestCurrentTriggerInstanceCarriesThePlatformAndTheGuild(t *testing.T) {
	ctx := withOrigin(context.Background(), "guild-1", "chan-1")

	instance, err := currentTriggerInstance(ctx)
	if err != nil {
		t.Fatalf("currentTriggerInstance: %v", err)
	}
	if instance == nil {
		t.Fatal("currentTriggerInstance returned no instance and no error")
	}

	if !instance.HasPlatformEnum() {
		t.Error("platform_enum is absent; the server refuses the request")
	}
	if !instance.HasInstanceMeta() {
		t.Error("instance_meta is absent; the server refuses the request")
	}
	if got := instance.GetPlatformEnum(); got != pb.Platform_PLATFORM_DISCORD {
		t.Errorf("platform_enum = %v, want %v", got, pb.Platform_PLATFORM_DISCORD)
	}

	want := callermeta.Origin{InstanceUID: "guild-1", DestinationUID: "chan-1"}.InstanceMeta()
	if !proto.Equal(instance.GetInstanceMeta(), want) {
		t.Errorf("instance_meta = %v, want %v", instance.GetInstanceMeta(), want)
	}
}

// TestCurrentTriggerInstanceRefusesAContextWithNoOrigin: a DM has no guild to
// scope to. FailedPrecondition, since errorMessage passes it through verbatim.
func TestCurrentTriggerInstanceRefusesAContextWithNoOrigin(t *testing.T) {
	_, err := currentTriggerInstance(context.Background())
	if err == nil {
		t.Fatal("a context with no origin produced an instance")
	}

	connErr := asConnectError(t, err)
	if connErr.Code() != connect.CodeFailedPrecondition {
		t.Errorf("code = %s, want %s (message %q)", connErr.Code(), connect.CodeFailedPrecondition, connErr.Message())
	}
	if connErr.Message() == "" {
		t.Error("the refusal has no message, so the user is shown an empty reply")
	}
}

func TestTriggerAddRefusesATriggerWithNothingToSay(t *testing.T) {
	err := invokeTrigger(t, triggerAddCommand(), map[string]any{"phrase": "gm"})
	requireInvalidArgument(t, err, "reply", "file")
}

// The refusal must list the accepted words: command.ArgType cannot render a
// choice list, so the vocabulary appears nowhere in the Discord UI.
func TestTriggerCommandsRefuseAnUnknownMode(t *testing.T) {
	tests := []struct {
		name string
		cmd  command.Command
		args map[string]any
	}{
		{
			name: "triggeradd",
			cmd:  triggerAddCommand(),
			args: map[string]any{"phrase": "gm", "reply": "good morning", "mode": "sometimes"},
		},
		{
			name: "triggermod",
			cmd:  triggerModCommand(),
			args: map[string]any{"id": triggerID, "mode": "sometimes"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := invokeTrigger(t, tt.cmd, tt.args)
			requireInvalidArgument(t, err, "exact", "any", "regex")
		})
	}
}

func TestTriggerStatsRefusesAnUnknownKind(t *testing.T) {
	err := invokeTrigger(t, triggerStatsCommand(), map[string]any{"kind": "everything"})
	requireInvalidArgument(t, err, "occurred", "called")
}

func TestTriggerCommandsRefuseAnOutOfRangeChance(t *testing.T) {
	overMax := int64(trigger.MaxChance) + 1
	bounds := []string{"0", fmt.Sprint(trigger.MaxChance)}

	tests := []struct {
		name string
		cmd  command.Command
		args map[string]any
	}{
		{
			name: "triggeradd above the maximum",
			cmd:  triggerAddCommand(),
			args: map[string]any{"phrase": "gm", "reply": "good morning", "chance": overMax},
		},
		{
			name: "triggeradd below zero",
			cmd:  triggerAddCommand(),
			args: map[string]any{"phrase": "gm", "reply": "good morning", "chance": int64(-1)},
		},
		{
			name: "triggermod above the maximum",
			cmd:  triggerModCommand(),
			args: map[string]any{"id": triggerID, "chance": overMax},
		},
		{
			name: "triggermod below zero",
			cmd:  triggerModCommand(),
			args: map[string]any{"id": triggerID, "chance": int64(-1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := invokeTrigger(t, tt.cmd, tt.args)
			requireInvalidArgument(t, err, bounds...)
		})
	}
}

// fakeTriggerClient captures the request a handler built, so an outgoing
// request's shape can be asserted without a server.
type fakeTriggerClient struct {
	ginbotv1connect.TriggerServiceClient

	update *pb.UpdateTriggerReq
	create *pb.CreateTriggerReq
	list   *pb.ListTriggersReq
	stats  *pb.GetTriggerStatsReq

	listCalls int

	err error
}

func (f *fakeTriggerClient) UpdateTrigger(_ context.Context, in *connect.Request[pb.UpdateTriggerReq]) (*connect.Response[pb.UpdateTriggerResp], error) {
	f.update = in.Msg
	if f.err != nil {
		return nil, f.err
	}

	return connect.NewResponse(pb.UpdateTriggerResp_builder{}.Build()), nil
}

func (f *fakeTriggerClient) CreateTrigger(_ context.Context, in *connect.Request[pb.CreateTriggerReq]) (*connect.Response[pb.CreateTriggerResp], error) {
	f.create = in.Msg
	if f.err != nil {
		return nil, f.err
	}

	id := triggerID

	return connect.NewResponse(pb.CreateTriggerResp_builder{Id: &id}.Build()), nil
}

func (f *fakeTriggerClient) ListTriggers(_ context.Context, in *connect.Request[pb.ListTriggersReq]) (*connect.Response[pb.ListTriggersResp], error) {
	f.list = in.Msg
	f.listCalls++
	if f.err != nil {
		return nil, f.err
	}

	return connect.NewResponse(pb.ListTriggersResp_builder{}.Build()), nil
}

func (f *fakeTriggerClient) GetTriggerStats(_ context.Context, in *connect.Request[pb.GetTriggerStatsReq]) (*connect.Response[pb.GetTriggerStatsResp], error) {
	f.stats = in.Msg
	if f.err != nil {
		return nil, f.err
	}

	return connect.NewResponse(pb.GetTriggerStatsResp_builder{}.Build()), nil
}

// Clients travel through the context, as commandContext does in production.
func guildContext(clients *client.Clients) context.Context {
	ctx := withOrigin(context.Background(), "guild-1", "channel-1")
	return withClients(ctx, clients)
}

func invokeNamed(t *testing.T, cmd command.Command, ctx context.Context, args map[string]any) (*command.Response, error) {
	t.Helper()

	inv, err := command.BindNamed(cmd, args)
	if err != nil {
		t.Fatalf("BindNamed: %v", err)
	}

	return cmd.Handler(ctx, inv)
}

// An omitted chance must arrive unset: 0 is the stored "use the default"
// sentinel, so sending it would rewrite a tuned trigger (ADR-0021).
func TestModifyTriggerSendsOnlyTheSuppliedFields(t *testing.T) {
	tests := []struct {
		name        string
		args        map[string]any
		wantPhrase  bool
		wantReply   bool
		wantFile    bool
		wantChance  bool
		wantMode    bool
		chanceValue int32
	}{
		{
			name:      "editing only the reply touches nothing else",
			args:      map[string]any{"id": triggerID, "reply": "good morning"},
			wantReply: true,
		},
		{
			name:       "editing only the chance touches nothing else",
			args:       map[string]any{"id": triggerID, "chance": int64(80)},
			wantChance: true, chanceValue: 80,
		},
		{
			// The sentinel is a legitimate value to SEND when it was typed: it
			// asks for the default back. Only an OMITTED chance must be unset.
			name:       "an explicit zero chance is sent",
			args:       map[string]any{"id": triggerID, "chance": int64(0)},
			wantChance: true, chanceValue: 0,
		},
		{
			name:     "editing only the mode touches nothing else",
			args:     map[string]any{"id": triggerID, "mode": "exact"},
			wantMode: true,
		},
		{
			name:       "editing only the phrase touches nothing else",
			args:       map[string]any{"id": triggerID, "phrase": "gn"},
			wantPhrase: true,
		},
		{
			name:     "editing only the file touches nothing else",
			args:     map[string]any{"id": triggerID, "file": "https://cdn.discordapp.com/x.png"},
			wantFile: true,
		},
		{
			// Chat arguments bind positionally, so "" is a placeholder for an
			// earlier argument, not a request to clear the field.
			name:     "empty placeholders are not changes",
			args:     map[string]any{"id": triggerID, "phrase": "", "reply": "", "file": "", "mode": "regex"},
			wantMode: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeTriggerClient{}
			ctx := guildContext(&client.Clients{Trigger: fake})

			if _, err := invokeNamed(t, triggerModCommand(), ctx, tt.args); err != nil {
				t.Fatalf("handler returned %v", err)
			}
			if fake.update == nil {
				t.Fatal("no UpdateTrigger request was sent")
			}

			got := fake.update
			if got.HasPhrase() != tt.wantPhrase {
				t.Errorf("HasPhrase() = %v, want %v", got.HasPhrase(), tt.wantPhrase)
			}
			if got.HasReply() != tt.wantReply {
				t.Errorf("HasReply() = %v, want %v", got.HasReply(), tt.wantReply)
			}
			if got.HasFileUrl() != tt.wantFile {
				t.Errorf("HasFileUrl() = %v, want %v", got.HasFileUrl(), tt.wantFile)
			}
			if got.HasChance() != tt.wantChance {
				t.Errorf("HasChance() = %v, want %v", got.HasChance(), tt.wantChance)
			}
			if got.HasMode() != tt.wantMode {
				t.Errorf("HasMode() = %v, want %v", got.HasMode(), tt.wantMode)
			}
			if tt.wantChance && got.GetChance() != tt.chanceValue {
				t.Errorf("GetChance() = %d, want %d", got.GetChance(), tt.chanceValue)
			}
			if got.GetId() != triggerID {
				t.Errorf("GetId() = %q, want %q", got.GetId(), triggerID)
			}
		})
	}
}

// The server would accept an empty patch, change nothing and report success.
func TestModifyTriggerRefusesAnEmptyPatch(t *testing.T) {
	for _, args := range []map[string]any{
		{"id": triggerID},
		{"id": triggerID, "reply": "", "phrase": "", "file": ""},
	} {
		fake := &fakeTriggerClient{}
		ctx := guildContext(&client.Clients{Trigger: fake})

		_, err := invokeNamed(t, triggerModCommand(), ctx, args)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("args %v: code = %v, want InvalidArgument", args, connect.CodeOf(err))
		}
		if fake.update != nil {
			t.Errorf("args %v: an empty patch was sent to the server anyway", args)
		}
	}
}

// Refused before the RPC, so no CDN fetch happens.
func TestAddTriggerRefusesBothAReplyAndAFile(t *testing.T) {
	fake := &fakeTriggerClient{}
	ctx := guildContext(&client.Clients{Trigger: fake})

	_, err := invokeNamed(t, triggerAddCommand(), ctx, map[string]any{
		"phrase": "gm",
		"reply":  "good morning",
		"file":   "https://cdn.discordapp.com/x.png",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if fake.create != nil {
		t.Error("the request was sent anyway, so the server fetched the file")
	}
}

// Sending UNSPECIFIED explicitly would pin a default the server owns.
func TestAddTriggerLeavesAnUnchosenModeUnset(t *testing.T) {
	fake := &fakeTriggerClient{}
	ctx := guildContext(&client.Clients{Trigger: fake})

	if _, err := invokeNamed(t, triggerAddCommand(), ctx, map[string]any{
		"phrase": "gm",
		"reply":  "good morning",
	}); err != nil {
		t.Fatalf("handler returned %v", err)
	}
	if fake.create == nil {
		t.Fatal("no CreateTrigger request was sent")
	}
	if fake.create.HasMode() {
		t.Errorf("HasMode() = true (%v), want unset", fake.create.GetMode())
	}
	if fake.create.HasChance() {
		t.Errorf("HasChance() = true (%d), want unset", fake.create.GetChance())
	}
	if len(fake.create.GetInstances()) != 1 {
		t.Errorf("got %d instances, want exactly the caller's own", len(fake.create.GetInstances()))
	}
}

func TestExplainRegexRefusal(t *testing.T) {
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("regex triggers require CLEARANCE_MODERATOR clearance"))

	tests := []struct {
		name     string
		err      error
		mode     pb.TriggerMode
		wantCode connect.Code
		wantText string
	}{
		{
			name:     "a regex refusal is explained",
			err:      denied,
			mode:     pb.TriggerMode_TRIGGER_MODE_REGEX,
			wantCode: connect.CodeFailedPrecondition,
			wantText: "moderator",
		},
		{
			name:     "a refusal for another mode is left alone",
			err:      denied,
			mode:     pb.TriggerMode_TRIGGER_MODE_ANY,
			wantCode: connect.CodePermissionDenied,
		},
		{
			name:     "a regex request refused for another reason is left alone",
			err:      connect.NewError(connect.CodeNotFound, errors.New("trigger not found")),
			mode:     pb.TriggerMode_TRIGGER_MODE_REGEX,
			wantCode: connect.CodeNotFound,
		},
		{
			name:     "an unspecified mode is left alone",
			err:      denied,
			mode:     pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED,
			wantCode: connect.CodePermissionDenied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := explainRegexRefusal(tt.err, tt.mode)
			if connect.CodeOf(got) != tt.wantCode {
				t.Fatalf("code = %v, want %v", connect.CodeOf(got), tt.wantCode)
			}
			if tt.wantText != "" {
				connErr := asConnectError(t, got)
				if !strings.Contains(connErr.Message(), tt.wantText) {
					t.Errorf("message %q does not name %q", connErr.Message(), tt.wantText)
				}
			}
			if message := errorMessage(got); message == "" {
				t.Error("errorMessage produced nothing")
			}
		})
	}
}

func TestExplainedRegexRefusalReachesTheUser(t *testing.T) {
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("regex triggers require CLEARANCE_MODERATOR clearance"))

	explained := errorMessage(explainRegexRefusal(denied, pb.TriggerMode_TRIGGER_MODE_REGEX))
	if !strings.Contains(explained, regexClearanceRequirement) {
		t.Errorf("the user sees %q, which does not name %q", explained, regexClearanceRequirement)
	}

	generic := errorMessage(denied)
	if strings.Contains(generic, regexClearanceRequirement) {
		t.Errorf("premise broken: an unexplained PermissionDenied already says %q", regexClearanceRequirement)
	}
}

func TestRegexClearanceRequirementMatchesTheEnum(t *testing.T) {
	if want := "moderator"; regexClearanceRequirement != want {
		t.Errorf("regexClearanceRequirement = %q, want %q", regexClearanceRequirement, want)
	}
}

func TestClampTriggerLimit(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want int64
	}{
		{name: "omitted uses the default", args: map[string]any{}, want: triggerListDefaultLimit},
		{name: "zero uses the default", args: map[string]any{"limit": int64(0)}, want: triggerListDefaultLimit},
		{name: "negative uses the default", args: map[string]any{"limit": int64(-5)}, want: triggerListDefaultLimit},
		{name: "in range is honoured", args: map[string]any{"limit": int64(7)}, want: 7},
		{name: "the maximum is honoured", args: map[string]any{"limit": triggerListMaxLimit}, want: triggerListMaxLimit},
		{name: "over the maximum clamps", args: map[string]any{"limit": int64(1000)}, want: triggerListMaxLimit},
	}

	cmd := triggerListCommand()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := command.BindNamed(cmd, tt.args)
			if err != nil {
				t.Fatalf("BindNamed: %v", err)
			}
			if got := clampTriggerLimit(inv, triggerListDefaultLimit, triggerListMaxLimit); got != tt.want {
				t.Errorf("clampTriggerLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestTriggerListMaxLimitFitsOneMessage: triggerListMaxLimit rows must fit in one
// Discord message (maxChatContent).
func TestTriggerListMaxLimitFitsOneMessage(t *testing.T) {
	line := renderTriggerLine(triggerFor(0, pb.TriggerMode_TRIGGER_MODE_ANY, "ok", nil))

	worst := len("**Triggers**\n") + int(triggerListMaxLimit)*(len(line)+1)
	if worst > maxChatContent {
		t.Errorf("%d rows of %d characters is %d, over maxChatContent %d",
			triggerListMaxLimit, len(line), worst, maxChatContent)
	}
}

// An always-set mine would silently turn the listing into "your triggers", and
// still look correct because the caller's own triggers are in the answer.
func TestListTriggersOnlySetsMineWhenNarrowing(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		fake := &fakeTriggerClient{}
		ctx := guildContext(&client.Clients{Trigger: fake})

		if _, err := invokeNamed(t, triggerListCommand(), ctx, map[string]any{}); err != nil {
			t.Fatalf("handler returned %v", err)
		}
		if fake.list == nil {
			t.Fatal("no ListTriggers request was sent")
		}
		if fake.list.GetMine() {
			t.Error("GetMine() = true without the mine flag")
		}
		if fake.list.GetLimit() != triggerListDefaultLimit {
			t.Errorf("GetLimit() = %d, want %d", fake.list.GetLimit(), triggerListDefaultLimit)
		}
		if fake.list.HasPhrase() {
			t.Error("HasPhrase() = true without a search")
		}
	})

	t.Run("supplied", func(t *testing.T) {
		fake := &fakeTriggerClient{}
		ctx := guildContext(&client.Clients{Trigger: fake})

		if _, err := invokeNamed(t, triggerListCommand(), ctx, map[string]any{"mine": true}); err != nil {
			t.Fatalf("handler returned %v", err)
		}
		if fake.list == nil {
			t.Fatal("no ListTriggers request was sent")
		}
		if !fake.list.GetMine() {
			t.Error("GetMine() = false with the mine flag set")
		}
	})

	t.Run("explicitly false", func(t *testing.T) {
		fake := &fakeTriggerClient{}
		ctx := guildContext(&client.Clients{Trigger: fake})

		if _, err := invokeNamed(t, triggerListCommand(), ctx, map[string]any{"mine": false}); err != nil {
			t.Fatalf("handler returned %v", err)
		}
		if fake.list == nil {
			t.Fatal("no ListTriggers request was sent")
		}
		if fake.list.GetMine() {
			t.Error("GetMine() = true for an explicit --mine=false")
		}
	})
}

type countingUserClient struct {
	ginbotv1connect.UserServiceClient

	getUserCalls int
}

func (c *countingUserClient) GetUser(_ context.Context, _ *connect.Request[pb.GetUserReq]) (*connect.Response[pb.GetUserResp], error) {
	c.getUserCalls++

	id := "0192f000-0000-7000-8000-0000000000aa"
	return connect.NewResponse(pb.GetUserResp_builder{User: pb.User_builder{Id: &id}.Build()}.Build()), nil
}

// Counted rather than inspected: a reintroduced GetUser round trip would
// produce exactly the same listing.
func TestListTriggersWithMineMakesExactlyOneRPC(t *testing.T) {
	fake := &fakeTriggerClient{}
	users := &countingUserClient{}
	ctx := guildContext(&client.Clients{Trigger: fake, User: users})

	if _, err := invokeNamed(t, triggerListCommand(), ctx, map[string]any{"mine": true}); err != nil {
		t.Fatalf("handler returned %v", err)
	}

	if fake.listCalls != 1 {
		t.Errorf("ListTriggers called %d times, want exactly 1", fake.listCalls)
	}
	if users.getUserCalls != 0 {
		t.Errorf("GetUser called %d times for a --mine listing, want 0; "+
			"the caller's own id is resolved from metadata server-side, so fetching it first is a round trip for nothing",
			users.getUserCalls)
	}
	if fake.list == nil {
		t.Fatal("no ListTriggers request was sent")
	}
	if !fake.list.GetMine() {
		t.Error("GetMine() = false; the single RPC did not carry the narrowing")
	}
}

func TestTriggerStatsScopesToTheCallersInstance(t *testing.T) {
	fake := &fakeTriggerClient{}
	ctx := guildContext(&client.Clients{Trigger: fake})

	if _, err := invokeNamed(t, triggerStatsCommand(), ctx, map[string]any{"kind": "called"}); err != nil {
		t.Fatalf("handler returned %v", err)
	}
	if fake.stats == nil {
		t.Fatal("no GetTriggerStats request was sent")
	}
	if !fake.stats.HasInstance() {
		t.Fatal("the request named no instance")
	}
	if got := fake.stats.GetActionType(); got != pb.ActionType_ACTION_TYPE_TRIGGER_CALLED {
		t.Errorf("GetActionType() = %v, want TRIGGER_CALLED", got)
	}
	if got := fake.stats.GetLimit(); got != triggerStatsDefaultLimit {
		t.Errorf("GetLimit() = %d, want %d", got, triggerStatsDefaultLimit)
	}
}

func TestTriggerCommandsRefuseADirectMessage(t *testing.T) {
	commands := map[string]command.Command{
		"add":   triggerAddCommand(),
		"stats": triggerStatsCommand(),
		"exec":  triggerExecCommand(),
	}
	args := map[string]map[string]any{
		"add":   {"phrase": "gm", "reply": "hi"},
		"stats": {},
		"exec":  {"id": triggerID},
	}

	for name, cmd := range commands {
		t.Run(name, func(t *testing.T) {
			fake := &fakeTriggerClient{}

			// An origin with no guild is what a DM delivers.
			ctx := withOrigin(context.Background(), "", "dm-channel")
			ctx = withClients(ctx, &client.Clients{Trigger: fake})

			_, err := invokeNamed(t, cmd, ctx, args[name])
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("code = %v, want FailedPrecondition", connect.CodeOf(err))
			}
			if message := errorMessage(err); message == "Something went wrong." {
				t.Error("the refusal reached the user as a generic failure")
			}
		})
	}
}

// A row can hold both, and the server fires the reply.
func TestTriggerRenderingPrefersTheReplyWhenBothAreSet(t *testing.T) {
	file := triggerFileFor(triggerFileID, "cat.png", "image/png")
	both := triggerFor(0, pb.TriggerMode_TRIGGER_MODE_ANY, "good morning", file)

	for name, rendered := range map[string]string{
		"info": formatTriggerInfo(both),
		"line": renderTriggerLine(both),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(rendered, "good morning") {
				t.Errorf("%q does not show the reply the trigger actually fires", rendered)
			}
			if strings.Contains(rendered, "cat.png") {
				t.Errorf("%q shows a file the trigger never plays", rendered)
			}
		})
	}
}

// Slow makes runInteraction acknowledge within Discord's 3s deadline before
// running a handler whose server side fetches from a CDN.
func TestCommandsWhoseServerFetchesMediaAreMarkedSlow(t *testing.T) {
	slow := map[string]bool{
		"triggeradd": true,
		"triggermod": true,
	}

	for _, cmd := range commandDefinitions() {
		want := slow[cmd.Name]
		if cmd.Slow != want {
			t.Errorf("%s: Slow = %v, want %v", cmd.Name, cmd.Slow, want)
		}
	}
}
