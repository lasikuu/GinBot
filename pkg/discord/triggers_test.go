package discord

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/trigger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// triggerID and triggerFileID are fixed so that an assertion about a rendering
// can name the string it expects to see in it.
const (
	triggerID     = "0192f000-0000-7000-8000-000000000001"
	triggerFileID = "0192f000-0000-7000-8000-0000000000ff"
)

// triggerFor builds a Trigger as GetTrigger returns one.
//
// chance is the STORED value, which is the whole point of the rendering
// assertions below: the stored column doubles as "unset", so it is not what a
// user should be shown.
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
	// chk_reply_or_file is an OR, NOT an exclusive one, so a row genuinely can
	// carry both a reply and a file. Passing both here is therefore a legitimate
	// fixture, and TestTriggerRenderingPrefersTheReplyWhenBothAreSet depends on
	// it being possible.
	if reply != "" {
		b.Reply = &reply
	}

	return b.Build()
}

// triggerFileFor builds the TriggerFile the server attaches to a fired trigger.
func triggerFileFor(fileID string, filename string, mimeType string) *pb.TriggerFile {
	byteSize := int64(16)

	return pb.TriggerFile_builder{
		FileId:   &fileID,
		Filename: &filename,
		MimeType: &mimeType,
		ByteSize: &byteSize,
	}.Build()
}

// statFor builds one leaderboard row as GetTriggerStats returns it.
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

// invokeTrigger binds named arguments and runs a trigger command's handler.
//
// The context is deliberately bare: it carries no origin and there is no gRPC
// connection. Every case that uses this must be refused by argument validation
// alone — a handler that resolved the instance first would answer
// FailedPrecondition instead of the message the user needs, and one that reached
// the RPC would nil-panic on client.TriggerServiceClient.
func invokeTrigger(t *testing.T, cmd command.Command, args map[string]any) error {
	t.Helper()

	inv, err := command.BindNamed(cmd, args)
	if err != nil {
		t.Fatalf("bind %s: %v", cmd.Name, err)
	}

	_, handlerErr := cmd.Handler(context.Background(), inv)

	return handlerErr
}

// requireInvalidArgument asserts a refusal the user can act on.
//
// The code matters as much as the wording: errorMessage passes only
// InvalidArgument and FailedPrecondition through verbatim, so a validation
// failure returned as any other code reaches the channel as "Something went
// wrong." and the user never learns what to type instead.
func requireInvalidArgument(t *testing.T, err error, mustMention ...string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error %v is not a gRPC status, so errorMessage cannot map it", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("code = %s, want %s (message %q)", st.Code(), codes.InvalidArgument, st.Message())
	}

	for _, word := range mustMention {
		if !strings.Contains(strings.ToLower(st.Message()), strings.ToLower(word)) {
			t.Errorf("message %q does not mention %q", st.Message(), word)
		}
	}
}

// ── declaration ──────────────────────────────────────────────────────────────

// TestTriggerCommandsAreGroupedUnderTrigger pins the Group/Sub assignment and
// the aliases. The Discord parent and every subcommand name are derived from
// Group and Sub, and the flat Name is what a chat invocation resolves — seven
// unrelated top-level commands is exactly what the group exists to avoid.
//
// The aliases are the names the previous bot used. An unknown chat command is
// ignored without a reply, so dropping one fails silently.
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
			// Creating a trigger is not an anonymous action: it is attributed to
			// a user account and only its creator may edit or delete it, so
			// there has to be one.
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

// TestTriggerCommandArguments pins the declared arguments, in order.
//
// Order is not cosmetic. Chat arguments bind POSITIONALLY, so ??triggeradd gm
// "good morning" only works while phrase is first, and Registry.Register refuses
// a required argument declared after an optional one — which is a log.Z.Fatal at
// boot, not a test failure. mode and chance are a string and an int because
// command.ArgType has no enum kind; the handler validates them.
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
				// The description is the only place a user learns what to type,
				// and Discord rejects an empty one at boot.
				if arg.Description == "" {
					t.Errorf("%q has no description", arg.Name)
				}
			}
		})
	}
}

// TestTriggerAliasesResolve is the chat half of the aliases: they only do
// anything if the registry actually resolves them, folded like every other name.
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

// TestTriggerCommandsGenerateOneGroupedCommand is the /trigger shape: seven
// registered commands, ONE top-level Discord command, seven subcommands. A
// member escaping back to top level is the regression to catch, since that is
// what the pre-group world looked like.
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
		// A group parent is purely a container: Discord rejects a command that
		// mixes subcommands with ordinary options.
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

// TestTriggerSubCommandsCarryTheirOwnArguments: a slash invocation delivers a
// subcommand's arguments as the SUBCOMMAND's options, so that is where they have
// to be declared. On the parent they would both be rejected by Discord and leave
// the handler with nothing bound.
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

			// Name, type and requiredness are derived from the declared Arg, so
			// a slash option and a chat positional cannot disagree.
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

// TestChatResolvesEveryTriggerSubcommand ties the generated slash surface back to
// the chat one: /trigger add and ??trigger add must reach the same handler, and
// the flat names must keep working alongside both.
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
			// The subcommand token is consumed, so binding sees the same
			// arguments the flat invocation would.
			if !slices.Equal(rest, []string{"gm", "good morning"}) {
				t.Errorf("remaining arguments = %q, want the two after the subcommand", rest)
			}
		})
	}
}

// ── mode and kind vocabularies ───────────────────────────────────────────────

// TestParseTriggerMode pins the user-facing vocabulary for mode.
//
// The empty string is the load-bearing case: command.ArgType has no enum kind,
// so an unsupplied optional string argument is indistinguishable from an empty
// one, and "" must therefore mean "not specified" rather than being refused —
// otherwise /triggeradd without a mode could never be run at all.
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
			// The accepted words are a user-facing vocabulary, not the wire
			// enum. Accepting the enum name would document one spelling in the
			// error message and silently honour another.
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

// TestTriggerModeNamesListsEveryAcceptedMode: the list is what a refusal shows
// the user, so a mode that parses but is not named leaves them guessing, and a
// name that does not parse sends them in circles.
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

// TestParseStatsKind pins the vocabulary for the leaderboard kind. "" defaults to
// the occurred board, because that is the one a bare /triggerstats means: an
// organic fire is the interesting statistic, a forced one is somebody poking the
// bot.
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
			// The server refuses anything but the two trigger action types, so a
			// reminder action must not be reachable from here either.
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

// TestStatsKindNamesListsEveryAcceptedKind is TestTriggerModeNamesListsEveryAcceptedMode
// for the leaderboard, and exists for the same reason.
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

// ── rendering ────────────────────────────────────────────────────────────────

// TestTriggerRenderingShowsTheEffectiveChance is ADR-0021 made visible.
//
// The stored chance column doubles as "unset": a stored 0 means the default, and
// exact mode is weighted on top of that. Rendering the stored value therefore
// tells the user something that is actively false — "0%" for a trigger that fires
// one message in twenty, and "40%" for one that always fires. Both views share
// the assertion because both are read by the same user in the same session.
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
			// Guard the premise: if the engine's weighting changed, the
			// assertion below would be pinning a stale number rather than the
			// rule.
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

// TestTriggerRenderingCarriesTheIdentifyingFields: the id is what /triggerinfo,
// /triggermod, /triggerdel and /triggerexec are all given by hand afterwards, so
// a list line without one is a dead end, and the phrase is the only way to tell
// two lines apart.
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

// TestFormatTriggerInfoShowsTheModeAndWhetherThereIsAFile: mode and chance
// together are what explain why a trigger did or did not fire, and "does this
// thing post a file into my guild" is the other question the detail view exists
// to answer.
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

	// Everything but the file is identical, so the two renderings can only
	// differ if the file is actually reported.
	withFile := formatTriggerInfo(triggerFor(10, pb.TriggerMode_TRIGGER_MODE_ANY, "",
		triggerFileFor(triggerFileID, "cat.png", "image/png")))
	withoutFile := formatTriggerInfo(triggerFor(10, pb.TriggerMode_TRIGGER_MODE_ANY, "", nil))

	if withFile == withoutFile {
		t.Errorf("a trigger with a file renders identically to one without:\n%s", withFile)
	}
}

// TestTriggerRenderingIsNilSafe. Both renderers walk a protobuf the server built
// row by row, and discordgo dispatches handlers without a recover(), so a nil
// deref in a renderer takes the process down rather than failing one command.
// The assertion is that these return at all.
func TestTriggerRenderingIsNilSafe(t *testing.T) {
	formatTriggerInfo(nil)
	renderTriggerLine(nil)
	formatTriggerStats(nil, pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED)
	formatTriggerStats([]*pb.TriggerStat{nil}, pb.ActionType_ACTION_TYPE_TRIGGER_CALLED)
}

// TestFormatTriggerStatsNamesTheKindAndRendersEveryRow: the two leaderboards are
// different questions — what fired on its own versus what somebody made fire —
// and they are rendered by the same function from the same shape, so a board that
// does not say which one it is is unreadable. One line per row, because a
// leaderboard that collapses or repeats rows is worse than none.
//
// The wording is deliberately not pinned: the reply may name the counter in the
// user's words rather than the enum's. What it may not do is render the two
// boards identically.
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

// TestFormatTriggerStatsSaysWhenNothingIsRecorded: a fresh guild has no rows, and
// a bare title with nothing under it reads as a broken command rather than as an
// empty leaderboard. The same applies to the nil slice, which is what a
// GetTriggerStats response with no rows actually decodes to.
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

			// The exact wording is not pinned, only that the absence is stated.
			lower := strings.ToLower(got)
			if !strings.Contains(lower, "no") && !strings.Contains(lower, "nothing") {
				t.Errorf("an empty leaderboard does not say it is empty: %q", got)
			}

			// And it has to say which counter is empty, for the same reason the
			// populated board does.
			if other := formatTriggerStats(empty.stats, pb.ActionType_ACTION_TYPE_TRIGGER_CALLED); got == other {
				t.Errorf("both empty leaderboards read identically: %q", got)
			}
		})
	}
}

// ── the file reply ───────────────────────────────────────────────────────────

// TestTriggerFileResponseNamesAndCarriesTheAttachment is the playback half of a
// file trigger.
//
// The server never stores an original filename, so every name here is derived:
// the display name it computed, else the file id, else a constant. Nothing may
// fall through to an empty name, because Discord rejects a nameless attachment
// and the trigger would then fire as silence.
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

			// It has to survive the Discord renderer too: a name or type that is
			// dropped between the two is the same bug as never setting it.
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

// TestTriggerFileResponseAlwaysNamesTheAttachment: Discord rejects an attachment
// with a blank filename outright, and the file IS the reply, so an unnamed
// attachment means the trigger fires as silence. The server always sends at
// least an id, so this is the last resort — but the last resort is the one
// nobody exercises by hand.
//
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

// TestTriggerFileResponseIsNilSafe: TryTriggerResp's reply is a oneof and its
// file case is unset both when nothing fired and when the server could not
// explain the row it selected. This runs on the gateway path, where discordgo
// recovers nothing, so neither may panic and neither may post an empty file.
func TestTriggerFileResponseIsNilSafe(t *testing.T) {
	if files := responseFiles(triggerFileResponse(nil, nil)); len(files) != 0 {
		t.Errorf("a nil file produced %d attachments, want none", len(files))
	}
}

// ── the current instance ─────────────────────────────────────────────────────

// TestCurrentTriggerInstanceCarriesThePlatformAndTheGuild.
//
// Every trigger RPC is scoped to an instance, and the server refuses one whose
// platform_enum or instance_meta is absent — with InvalidArgument, which reaches
// the user verbatim. So presence is asserted, not just the values. The jsonb
// shape is compared against callermeta's own builder because instance rows are
// looked up by jsonb equality: a differently keyed struct silently creates a
// second instance row for the same guild.
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

// TestCurrentTriggerInstanceRefusesAContextWithNoOrigin: a trigger belongs to a
// guild, so there is nothing to scope one to in a direct message. It must be
// FailedPrecondition, because that is one of the two codes errorMessage lets
// through verbatim — anything else tells the user "Something went wrong." for a
// situation they could fix by running the command somewhere else.
func TestCurrentTriggerInstanceRefusesAContextWithNoOrigin(t *testing.T) {
	_, err := currentTriggerInstance(context.Background())
	if err == nil {
		t.Fatal("a context with no origin produced an instance")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error %v is not a gRPC status, so errorMessage cannot map it", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code = %s, want %s (message %q)", st.Code(), codes.FailedPrecondition, st.Message())
	}
	if st.Message() == "" {
		t.Error("the refusal has no message, so the user is shown an empty reply")
	}
}

// ── handler-level validation ─────────────────────────────────────────────────

// TestTriggerAddRefusesATriggerWithNothingToSay is acceptance criterion 3. The
// server enforces it too, but a round trip to be told an obvious thing is poor
// UX — and the message has to name BOTH ways out, or the user is left guessing
// which of the two optional arguments they were supposed to fill in.
func TestTriggerAddRefusesATriggerWithNothingToSay(t *testing.T) {
	err := invokeTrigger(t, triggerAddCommand(), map[string]any{"phrase": "gm"})
	requireInvalidArgument(t, err, "reply", "file")
}

// TestTriggerCommandsRefuseAnUnknownMode is acceptance criterion 5 for mode.
// Both commands take one, and both must list the accepted words: "invalid mode"
// on its own leaves the user to guess a vocabulary that exists nowhere in the
// Discord UI, since command.ArgType cannot render a choice list.
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

// TestTriggerStatsRefusesAnUnknownKind is acceptance criterion 5 for kind, and
// exists for the same reason.
func TestTriggerStatsRefusesAnUnknownKind(t *testing.T) {
	err := invokeTrigger(t, triggerStatsCommand(), map[string]any{"kind": "everything"})
	requireInvalidArgument(t, err, "occurred", "called")
}

// TestTriggerCommandsRefuseAnOutOfRangeChance: the column has a CHECK and the
// server validates it, so an out-of-range chance can only ever come back as an
// error — but the message must name the range, because "chance" has no visible
// bounds in the Discord UI and 0 already means something non-obvious.
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

// fakeTriggerClient captures the request a handler built, so the SHAPE of an
// outgoing request can be asserted without a server.
//
// It exists for one reason: UpdateTriggerReq is patch-shaped, the server branches
// on HasX(), and "sent a defaulted value for an argument the caller omitted" is
// the named worst-bug class in this codebase — editing a trigger's reply must not
// silently reset its chance. Nothing else can observe that but the request.
type fakeTriggerClient struct {
	pb.TriggerServiceClient

	update *pb.UpdateTriggerReq
	create *pb.CreateTriggerReq
	list   *pb.ListTriggersReq
	stats  *pb.GetTriggerStatsReq

	err error
}

func (f *fakeTriggerClient) UpdateTrigger(_ context.Context, in *pb.UpdateTriggerReq, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.update = in
	if f.err != nil {
		return nil, f.err
	}

	return &emptypb.Empty{}, nil
}

func (f *fakeTriggerClient) CreateTrigger(_ context.Context, in *pb.CreateTriggerReq, _ ...grpc.CallOption) (*pb.CreateTriggerResp, error) {
	f.create = in
	if f.err != nil {
		return nil, f.err
	}

	id := triggerID

	return pb.CreateTriggerResp_builder{Id: &id}.Build(), nil
}

func (f *fakeTriggerClient) ListTriggers(_ context.Context, in *pb.ListTriggersReq, _ ...grpc.CallOption) (*pb.ListTriggersResp, error) {
	f.list = in
	if f.err != nil {
		return nil, f.err
	}

	return pb.ListTriggersResp_builder{}.Build(), nil
}

func (f *fakeTriggerClient) GetTriggerStats(_ context.Context, in *pb.GetTriggerStatsReq, _ ...grpc.CallOption) (*pb.GetTriggerStatsResp, error) {
	f.stats = in
	if f.err != nil {
		return nil, f.err
	}

	return pb.GetTriggerStatsResp_builder{}.Build(), nil
}

// withFakeTriggerClient swaps the package-level client for the duration of a
// test and restores it, so tests stay order-independent.
func withFakeTriggerClient(t *testing.T, fake *fakeTriggerClient) {
	t.Helper()

	previous := client.TriggerServiceClient
	client.TriggerServiceClient = fake
	t.Cleanup(func() { client.TriggerServiceClient = previous })
}

// guildContext is the context a handler receives for a command typed in a guild.
func guildContext() context.Context {
	return withOrigin(context.Background(), "guild-1", "channel-1")
}

// invokeNamed runs a command's handler with named arguments, the way a slash
// invocation delivers them.
func invokeNamed(t *testing.T, cmd command.Command, ctx context.Context, args map[string]any) (*command.Response, error) {
	t.Helper()

	inv, err := command.BindNamed(cmd, args)
	if err != nil {
		t.Fatalf("BindNamed: %v", err)
	}

	return cmd.Handler(ctx, inv)
}

// TestModifyTriggerSendsOnlyTheSuppliedFields is the test the patch shape exists
// for.
//
// UpdateTriggerReq is patch-shaped: the server leaves a field alone when HasX()
// is false. So an omitted chance must arrive UNSET, not as 0 — and 0 is not even
// a neutral value here, it is the stored sentinel meaning "use the default"
// (ADR-0021), so sending it would rewrite a tuned 80% trigger to the default 5%
// every time somebody edited its reply.
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
			// Chat arguments bind positionally, so changing a later argument
			// means typing "" for the earlier ones. An empty placeholder is not
			// a request to clear the field — and for reply it MUST not be, because
			// the server writes NULL and then chk_reply_or_file fails as a bare
			// codes.Internal.
			name:     "empty placeholders are not changes",
			args:     map[string]any{"id": triggerID, "phrase": "", "reply": "", "file": "", "mode": "regex"},
			wantMode: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeTriggerClient{}
			withFakeTriggerClient(t, fake)

			if _, err := invokeNamed(t, triggerModCommand(), guildContext(), tt.args); err != nil {
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

// An id with nothing to change must be refused rather than sent. The server
// would accept it, change nothing and report success, which reads to the user as
// an edit that did not stick.
func TestModifyTriggerRefusesAnEmptyPatch(t *testing.T) {
	for _, args := range []map[string]any{
		{"id": triggerID},
		{"id": triggerID, "reply": "", "phrase": "", "file": ""},
	} {
		fake := &fakeTriggerClient{}
		withFakeTriggerClient(t, fake)

		_, err := invokeNamed(t, triggerModCommand(), guildContext(), args)
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("args %v: code = %v, want InvalidArgument", args, status.Code(err))
		}
		if fake.update != nil {
			t.Errorf("args %v: an empty patch was sent to the server anyway", args)
		}
	}
}

// A trigger answers with a reply or a file. Both at once is storable —
// chk_reply_or_file is an OR — and the server fires the REPLY, so the file would
// be paid for and never played. Refused before the RPC, so no CDN fetch happens.
func TestAddTriggerRefusesBothAReplyAndAFile(t *testing.T) {
	fake := &fakeTriggerClient{}
	withFakeTriggerClient(t, fake)

	_, err := invokeNamed(t, triggerAddCommand(), guildContext(), map[string]any{
		"phrase": "gm",
		"reply":  "good morning",
		"file":   "https://cdn.discordapp.com/x.png",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
	if fake.create != nil {
		t.Error("the request was sent anyway, so the server fetched the file")
	}
}

// The mode is only sent when the caller chose one: UNSPECIFIED is what "did not
// choose" looks like, and the server applies its own default for it. Sending it
// explicitly would work today but pins a default the server owns.
func TestAddTriggerLeavesAnUnchosenModeUnset(t *testing.T) {
	fake := &fakeTriggerClient{}
	withFakeTriggerClient(t, fake)

	if _, err := invokeNamed(t, triggerAddCommand(), guildContext(), map[string]any{
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

// explainRegexRefusal is the only client-side error REWRITING in this surface,
// and it is what makes acceptance criterion 4 readable: errorMessage flattens
// PermissionDenied to "You are not allowed to do that." but passes a
// FailedPrecondition message through verbatim.
//
// The guard matters as much as the rewrite. It must not explain an unrelated
// refusal as a clearance problem, and it must not touch a non-regex request.
func TestExplainRegexRefusal(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "regex triggers require CLEARANCE_MODERATOR clearance")

	tests := []struct {
		name     string
		err      error
		mode     pb.TriggerMode
		wantCode codes.Code
		wantText string
	}{
		{
			name:     "a regex refusal is explained",
			err:      denied,
			mode:     pb.TriggerMode_TRIGGER_MODE_REGEX,
			wantCode: codes.FailedPrecondition,
			wantText: "moderator",
		},
		{
			name:     "a refusal for another mode is left alone",
			err:      denied,
			mode:     pb.TriggerMode_TRIGGER_MODE_ANY,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "a regex request refused for another reason is left alone",
			err:      status.Error(codes.NotFound, "trigger not found"),
			mode:     pb.TriggerMode_TRIGGER_MODE_REGEX,
			wantCode: codes.NotFound,
		},
		{
			name:     "an unspecified mode is left alone",
			err:      denied,
			mode:     pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED,
			wantCode: codes.PermissionDenied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := explainRegexRefusal(tt.err, tt.mode)
			if status.Code(got) != tt.wantCode {
				t.Fatalf("code = %v, want %v", status.Code(got), tt.wantCode)
			}
			if tt.wantText != "" && !strings.Contains(status.Convert(got).Message(), tt.wantText) {
				t.Errorf("message %q does not name %q", status.Convert(got).Message(), tt.wantText)
			}
			// Whatever the outcome, the user must never see a raw code name.
			if message := errorMessage(got); message == "" {
				t.Error("errorMessage produced nothing")
			}
		})
	}
}

// The explained refusal has to survive errorMessage, which is the only thing
// standing between a gRPC status and the channel. A PermissionDenied would be
// flattened to a message that names no requirement at all.
func TestExplainedRegexRefusalReachesTheUser(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "regex triggers require CLEARANCE_MODERATOR clearance")

	explained := errorMessage(explainRegexRefusal(denied, pb.TriggerMode_TRIGGER_MODE_REGEX))
	if !strings.Contains(explained, regexClearanceRequirement) {
		t.Errorf("the user sees %q, which does not name %q", explained, regexClearanceRequirement)
	}

	generic := errorMessage(denied)
	if strings.Contains(generic, regexClearanceRequirement) {
		t.Errorf("premise broken: an unexplained PermissionDenied already says %q", regexClearanceRequirement)
	}
}

// The clearance word is derived from the enum rather than typed out, so it cannot
// drift from the floor the server actually enforces.
func TestRegexClearanceRequirementMatchesTheEnum(t *testing.T) {
	if want := "moderator"; regexClearanceRequirement != want {
		t.Errorf("regexClearanceRequirement = %q, want %q", regexClearanceRequirement, want)
	}
}

// clampTriggerLimit clamps rather than refuses, because someone typing 1000 means
// "as many as you can". The maximum has to fit inside maxChatContent, or asking
// for the advertised maximum produces the truncated reply the cap exists to
// avoid.
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

// A list of triggerListMaxLimit rows has to fit in one Discord message, because
// the cap's stated reason is that "a reply cut off mid-line is worse than a reply
// that says less". A 36-character UUID plus the separators is the floor per line.
func TestTriggerListMaxLimitFitsOneMessage(t *testing.T) {
	line := renderTriggerLine(triggerFor(0, pb.TriggerMode_TRIGGER_MODE_ANY, "ok", nil))

	worst := len("**Triggers**\n") + int(triggerListMaxLimit)*(len(line)+1)
	if worst > maxChatContent {
		t.Errorf("%d rows of %d characters is %d, over maxChatContent %d",
			triggerListMaxLimit, len(line), worst, maxChatContent)
	}
}

// mine narrows the list to the caller's own triggers. The client does not know
// its own UUID, so it has to resolve one — and it must not send an empty id,
// which the server compares against the caller and refuses as PermissionDenied.
func TestListTriggersOnlySendsAUserIdWhenNarrowing(t *testing.T) {
	fake := &fakeTriggerClient{}
	withFakeTriggerClient(t, fake)

	if _, err := invokeNamed(t, triggerListCommand(), guildContext(), map[string]any{}); err != nil {
		t.Fatalf("handler returned %v", err)
	}
	if fake.list == nil {
		t.Fatal("no ListTriggers request was sent")
	}
	if fake.list.HasUserId() {
		t.Errorf("HasUserId() = true (%q) without mine", fake.list.GetUserId())
	}
	if fake.list.GetLimit() != triggerListDefaultLimit {
		t.Errorf("GetLimit() = %d, want %d", fake.list.GetLimit(), triggerListDefaultLimit)
	}
	if fake.list.HasPhrase() {
		t.Error("HasPhrase() = true without a search")
	}
}

// The stats request must name the caller's own instance and the parsed counter.
// The server refuses a leaderboard for any other instance, so an unset one is a
// FailedPrecondition rather than a wrong answer.
func TestTriggerStatsScopesToTheCallersInstance(t *testing.T) {
	fake := &fakeTriggerClient{}
	withFakeTriggerClient(t, fake)

	if _, err := invokeNamed(t, triggerStatsCommand(), guildContext(), map[string]any{"kind": "called"}); err != nil {
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

// A trigger command used in a direct message is refused before the RPC. The
// refusal has to be a FailedPrecondition, which errorMessage passes through
// verbatim, so the user is told to use it in a server rather than shown
// "Something went wrong."
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
			withFakeTriggerClient(t, fake)

			// An origin with no guild is exactly what a DM delivers.
			ctx := withOrigin(context.Background(), "", "dm-channel")

			_, err := invokeNamed(t, cmd, ctx, args[name])
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("code = %v, want FailedPrecondition", status.Code(err))
			}
			if message := errorMessage(err); message == "Something went wrong." {
				t.Error("the refusal reached the user as a generic failure")
			}
		})
	}
}

// A trigger carrying BOTH a reply and a file is storable, and the server fires
// the reply. So the rendering must show the reply — showing the file would tell
// the user their trigger does the opposite of what it does.
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

// Slow is what keeps a command whose server side fetches from a CDN inside
// Discord's three-second interaction deadline. Marking it is the only thing that
// makes runInteraction acknowledge before running the handler, so the flag is
// load-bearing and easy to lose in a refactor.
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
