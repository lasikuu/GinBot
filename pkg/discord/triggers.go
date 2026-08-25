package discord

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/trigger"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// triggerGroup nests the trigger commands under one Discord parent, so they
// appear as /trigger add rather than as seven unrelated top-level commands.
//
// The flat Name of each member is unchanged, so the old bot's ??triggeradd keeps
// working; ??trigger add reaches the same handler via Registry.ResolveChat.
const triggerGroup = "trigger"

// Sub names within triggerGroup. They are short because Discord shows them as
// the second word of /trigger <sub>, where the group already says "trigger".
const (
	triggerSubAdd   = "add"
	triggerSubDel   = "del"
	triggerSubMod   = "mod"
	triggerSubList  = "list"
	triggerSubInfo  = "info"
	triggerSubStats = "stats"
	triggerSubExec  = "exec"
)

// Limits on how much of a list one reply renders. Both are clamped rather than
// refused: someone typing 1000 means "as many as you can", not "fail".
//
// The list is capped lower than it could be because every trigger costs a line
// and a message is capped at maxChatContent; a reply cut off mid-line is worse
// than a reply that says less.
const (
	triggerListDefaultLimit int64 = 15
	triggerListMaxLimit     int64 = 25

	triggerStatsDefaultLimit int64 = 10
	triggerStatsMaxLimit     int64 = 25
)

// triggerFileTimeout bounds the GetFile call that pulls a fired trigger's bytes
// back. A command handler's context is rooted at Background with no deadline of
// its own, so without this an unresponsive server holds the handler open
// indefinitely.
//
// It is deliberately shorter than triggerAttemptTimeout, which is the whole
// budget on the message path: a nested deadline longer than the one enclosing it
// never expires and so bounds nothing.
const triggerFileTimeout = 10 * time.Second

// triggerCallTimeout bounds every other outgoing trigger RPC.
//
// None of them inherits a deadline: commandContext roots the handler context at
// context.Background, and no gRPC keepalive is configured, so a half-open
// connection would hold a handler — and its interaction — open forever.
// confirmDelivery bounds its own call for exactly this reason.
const triggerCallTimeout = 20 * time.Second

// unnamedTriggerFileName names an attachment whose file carries neither a
// filename nor an id. The server always sends at least an id, so this is a
// last resort that exists only because Discord rejects a blank filename.
const unnamedTriggerFileName = "trigger"

// regexClearanceRequirement names what a regex trigger needs, for the message
// the server's PermissionDenied is rewritten into.
//
// Derived from the enum rather than written out, so it cannot drift from the
// server's own regexClearanceFloor. The floor itself is not imported because
// pkg/grpc/server pulls in the database and is not reachable from a platform
// client; the server remains the sole authority on the decision, and this is
// only the wording of its refusal.
var regexClearanceRequirement = strings.ToLower(strings.TrimPrefix(
	pb.Clearance_CLEARANCE_MODERATOR.String(), "CLEARANCE_"))

// fileArgDescription says what the file argument actually takes. It is a URL,
// not an attachment: command.ArgType has no attachment kind, and the server
// fetches the bytes itself from an allow-listed CDN host (ADR-0007).
const fileArgDescription = "Discord CDN link to reply with, e.g. copied from an existing attachment"

// modeArgDescription documents the accepted mode words once, for both the create
// and the modify command, and is derived from the same table the parser uses so
// the two cannot drift.
var modeArgDescription = "How the phrase is matched: " + triggerModeNames() + " (default any)"

// chanceArgDescription documents the chance argument once, built from
// trigger.MaxChance so the text cannot claim a bound the server does not
// enforce. The sentinel is called out because a stored 0 meaning "the default"
// is not discoverable otherwise (ADR-0021).
var chanceArgDescription = fmt.Sprintf(
	"Percent chance of firing, 0-%d. 0 means the default, which exact mode triples",
	trigger.MaxChance,
)

// kindArgDescription documents the stats counter argument, derived from the same
// table parseStatsKind uses.
var kindArgDescription = "Which counter to rank by: " + statsKindNames()

// triggerModeWords maps the mode words a user may type onto the enum, in the
// order they are offered. A slice rather than a map: the accepted-values message
// has to read in a stable order, and map iteration does not.
var triggerModeWords = []struct {
	word string
	mode pb.TriggerMode
}{
	{"exact", pb.TriggerMode_TRIGGER_MODE_EXACT},
	{"any", pb.TriggerMode_TRIGGER_MODE_ANY},
	{"regex", pb.TriggerMode_TRIGGER_MODE_REGEX},
}

// statsKindWords maps the counter words a user may type onto the ActionType the
// leaderboard is built from, with the label the reply shows for each. The word
// and the label are separate because "occurred" is the enum's vocabulary and
// "random fires" is the user's.
var statsKindWords = []struct {
	word       string
	label      string
	actionType pb.ActionType
}{
	{"occurred", "random fires", pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED},
	{"called", "forced fires", pb.ActionType_ACTION_TYPE_TRIGGER_CALLED},
}

// boundedCall derives the context every trigger RPC is made on, and the cancel
// its caller must defer.
//
// A separate helper rather than an inline WithTimeout at each call site, so that
// a new trigger command cannot quietly be the one that forgets.
func boundedCall(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, triggerCallTimeout)
}

// logCallFailure logs a failed trigger RPC, at a level that matches whose fault
// it is.
//
// NotFound is the server's deliberate answer for "not yours or does not exist"
// — an ordinary outcome of a mistyped id, not an incident — so it stays at debug
// rather than filling the log with errors that need no action. runInteraction and
// dispatchChatCommand log the returned error again, so nothing is lost.
func logCallFailure(rpc string, err error) {
	if status.Code(err) == codes.NotFound {
		log.Z.Debug("trigger rpc found nothing.", zap.String("rpc", rpc))
		return
	}

	// Never the phrase, the reply or the id: a stored phrase must not reach a
	// log line, and an InvalidArgument message here can quote the pattern.
	log.Z.Error("trigger rpc failed.", zap.String("rpc", rpc), zap.Error(err))
}

// parseTriggerMode maps a user-supplied mode word onto a TriggerMode.
// An empty value yields TRIGGER_MODE_UNSPECIFIED with ok true, meaning "not
// specified"; the caller then leaves the field unset and the server defaults it.
// Matching is case-insensitive and surrounding space is trimmed.
func parseTriggerMode(value string) (pb.TriggerMode, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED, true
	}

	for _, candidate := range triggerModeWords {
		if strings.EqualFold(trimmed, candidate.word) {
			return candidate.mode, true
		}
	}

	return pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED, false
}

// triggerModeNames lists the accepted mode words for an error message.
func triggerModeNames() string {
	words := make([]string, 0, len(triggerModeWords))
	for _, candidate := range triggerModeWords {
		words = append(words, candidate.word)
	}

	return strings.Join(words, ", ")
}

// triggerModeName turns TRIGGER_MODE_EXACT into "exact" for display.
func triggerModeName(mode pb.TriggerMode) string {
	return strings.ToLower(strings.TrimPrefix(mode.String(), "TRIGGER_MODE_"))
}

// parseStatsKind maps a user-supplied kind word onto the leaderboard's
// ActionType. An empty value yields ACTION_TYPE_TRIGGER_OCCURRED.
func parseStatsKind(value string) (pb.ActionType, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED, true
	}

	for _, candidate := range statsKindWords {
		if strings.EqualFold(trimmed, candidate.word) {
			return candidate.actionType, true
		}
	}

	return pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED, false
}

// statsKindNames lists the accepted kind words for an error message.
func statsKindNames() string {
	words := make([]string, 0, len(statsKindWords))
	for _, candidate := range statsKindWords {
		words = append(words, candidate.word)
	}

	return strings.Join(words, ", ")
}

// statsKindLabel names a counter the way the leaderboard shows it.
func statsKindLabel(kind pb.ActionType) string {
	for _, candidate := range statsKindWords {
		if candidate.actionType == kind {
			return candidate.label
		}
	}

	return strings.ToLower(strings.TrimPrefix(kind.String(), "ACTION_TYPE_"))
}

func triggerAddCommand() command.Command {
	return command.Command{
		Name: "triggeradd",
		// triggercreate reads naturally for anyone who reached for it first.
		Aliases: []string{"triggercreate"},
		// No command name in the example: this text is shown both as the flat
		// command's description and as the /trigger add subcommand's.
		Description: "Create an auto-responder that fires when a phrase appears in chat",
		Group:       triggerGroup,
		Sub:         triggerSubAdd,
		Args: []command.Arg{
			{
				Name:        "phrase",
				Description: "What to look for in a message",
				Type:        command.ArgString,
				Required:    true,
			},
			{
				Name:        "reply",
				Description: "Text to reply with; required unless you give a file",
				Type:        command.ArgString,
			},
			{
				Name:        "file",
				Description: fileArgDescription,
				Type:        command.ArgString,
			},
			{
				Name:        "chance",
				Description: chanceArgDescription,
				Type:        command.ArgInt,
			},
			{
				Name:        "mode",
				Description: modeArgDescription,
				Type:        command.ArgString,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		// The server fetches the file argument from a CDN before it answers,
		// bounded at 30 seconds, which is ten times Discord's interaction
		// deadline. Acknowledged first, answered afterwards.
		Slow:    true,
		Handler: addTrigger,
	}
}

func addTrigger(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	phrase := inv.String("phrase")
	reply := strings.TrimSpace(inv.String("reply"))
	fileURL := strings.TrimSpace(inv.String("file"))

	// Everything the client can judge is judged BEFORE the instance lookup and
	// before the RPC. The server enforces all of it too, but a round trip that
	// only tells the user something obvious is a bad trade.
	if reply == "" && fileURL == "" {
		return nil, status.Errorf(codes.InvalidArgument,
			"a trigger needs either a reply or a file")
	}
	// Refused rather than silently resolved. chk_reply_or_file permits both
	// columns at once and the server fires the REPLY when both are set, so
	// accepting this would store a file that can never play and bill the caller
	// a CDN fetch for it. There is no way to express "both" in a single answer,
	// so the caller has to choose.
	if reply != "" && fileURL != "" {
		return nil, status.Errorf(codes.InvalidArgument,
			"a trigger answers with a reply or a file, not both")
	}

	mode, ok := parseTriggerMode(inv.String("mode"))
	if !ok {
		return nil, invalidModeError()
	}

	chance, err := triggerChance(inv)
	if err != nil {
		return nil, err
	}

	instance, err := currentTriggerInstance(ctx)
	if err != nil {
		return nil, err
	}

	b := pb.CreateTriggerReq_builder{
		Phrase:    &phrase,
		Instances: []*pb.TriggerInstance{instance},
	}
	if reply != "" {
		b.Reply = &reply
	}
	if fileURL != "" {
		b.FileUrl = &fileURL
	}
	// UNSPECIFIED is what "the caller did not choose" looks like, so it is left
	// unset and the server applies its own default.
	if mode != pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED {
		b.Mode = &mode
	}
	if chance != nil {
		b.Chance = chance
	}

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	resp, err := client.TriggerServiceClient.CreateTrigger(callCtx, b.Build())
	if err != nil {
		logCallFailure("CreateTrigger", err)
		return nil, explainRegexRefusal(err, mode)
	}

	return &command.Response{
		Content:   fmt.Sprintf("Trigger created: `%s`.", resp.GetId()),
		Ephemeral: true,
	}, nil
}

func triggerDelCommand() command.Command {
	return command.Command{
		Name:        "triggerdel",
		Aliases:     []string{"triggerremove"},
		Description: "Delete one of your triggers by id",
		Group:       triggerGroup,
		Sub:         triggerSubDel,
		Args: []command.Arg{
			{
				Name:        "id",
				Description: "Trigger id",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   deleteTrigger,
	}
}

// deleteTrigger names no instance: the server scopes ownership from the call
// origin, and another user's trigger comes back NotFound by design so that a
// caller cannot probe for ids. That is left as "Not found." rather than dressed
// up as a permission error.
func deleteTrigger(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	id := inv.String("id")

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	req := pb.DeleteTriggerReq_builder{Id: &id}.Build()
	if _, err := client.TriggerServiceClient.DeleteTrigger(callCtx, req); err != nil {
		logCallFailure("DeleteTrigger", err)
		return nil, err
	}

	return &command.Response{
		Content:   fmt.Sprintf("Trigger `%s` deleted.", id),
		Ephemeral: true,
	}, nil
}

func triggerModCommand() command.Command {
	return command.Command{
		Name:        "triggermod",
		Aliases:     []string{"triggeredit"},
		Description: "Change one of your triggers",
		Group:       triggerGroup,
		Sub:         triggerSubMod,
		Args: []command.Arg{
			{
				Name:        "id",
				Description: "Trigger id",
				Type:        command.ArgString,
				Required:    true,
			},
			{
				Name:        "phrase",
				Description: "New phrase to look for; omit to keep it",
				Type:        command.ArgString,
			},
			{
				Name:        "reply",
				Description: "New reply text; omit to keep it",
				Type:        command.ArgString,
			},
			{
				Name:        "file",
				Description: fileArgDescription,
				Type:        command.ArgString,
			},
			{
				Name:        "chance",
				Description: chanceArgDescription,
				Type:        command.ArgInt,
			},
			{
				Name:        "mode",
				Description: modeArgDescription,
				Type:        command.ArgString,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		// Slow for the same reason addTrigger is: a new file argument makes the
		// server fetch it before answering.
		Slow:    true,
		Handler: modifyTrigger,
	}
}

// modifyTrigger sends only the fields the caller actually supplied.
//
// UpdateTriggerReq is patch-shaped: the server branches on HasX(), so an unset
// field means "leave it alone". Sending a defaulted value instead would make
// editing a trigger's reply silently reset its chance to 0, which is the class of
// bug clearRepeatSentinel exists to avoid on the reminder side.
func modifyTrigger(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	id := inv.String("id")

	mode, ok := parseTriggerMode(inv.String("mode"))
	if !ok {
		return nil, invalidModeError()
	}

	chance, err := triggerChance(inv)
	if err != nil {
		return nil, err
	}

	b := pb.UpdateTriggerReq_builder{Id: &id}

	// Each field is sent only when it was supplied AND non-blank. Has() alone is
	// not enough: chat arguments bind positionally, so changing only the mode
	// means typing "" for everything before it, and a supplied-but-empty value
	// is that placeholder rather than a request.
	//
	// It also closes two holes. An empty file_url passes HasFileUrl() but the
	// server then skips the update, so the handler would report a change that
	// did not happen. An empty reply is written as NULL, which on a trigger with
	// no file violates chk_reply_or_file and surfaces as a bare
	// "Something went wrong." — clearing a reply is not an operation this
	// command offers, because a trigger with nothing to say cannot exist.
	if phrase := strings.TrimSpace(inv.String("phrase")); inv.Has("phrase") && phrase != "" {
		b.Phrase = &phrase
	}
	if reply := inv.String("reply"); inv.Has("reply") && strings.TrimSpace(reply) != "" {
		b.Reply = &reply
	}
	if fileURL := strings.TrimSpace(inv.String("file")); inv.Has("file") && fileURL != "" {
		b.FileUrl = &fileURL
	}
	if chance != nil {
		b.Chance = chance
	}
	if mode != pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED {
		b.Mode = &mode
	}

	// Refused here rather than sent: the server would accept an id-only update,
	// do nothing and report success, which reads to the user as a change that
	// did not stick.
	if b.Phrase == nil && b.Reply == nil && b.FileUrl == nil && b.Chance == nil && b.Mode == nil {
		return nil, status.Errorf(codes.InvalidArgument,
			"nothing to change: give at least one of phrase, reply, file, chance or mode")
	}

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	if _, err := client.TriggerServiceClient.UpdateTrigger(callCtx, b.Build()); err != nil {
		logCallFailure("UpdateTrigger", err)
		return nil, explainRegexRefusal(err, mode)
	}

	return &command.Response{
		Content:   fmt.Sprintf("Trigger `%s` updated.", id),
		Ephemeral: true,
	}, nil
}

func triggerListCommand() command.Command {
	return command.Command{
		Name:        "triggers",
		Aliases:     []string{"triggerlist"},
		Description: "List the triggers on this server",
		Group:       triggerGroup,
		Sub:         triggerSubList,
		Args: []command.Arg{
			{
				Name:        "search",
				Description: "Only triggers whose phrase contains this",
				Type:        command.ArgString,
			},
			{
				Name:        "mine",
				Description: "Only your own triggers",
				Type:        command.ArgBool,
			},
			{
				Name: "limit",
				Description: fmt.Sprintf("How many to show, up to %d (default %d)",
					triggerListMaxLimit, triggerListDefaultLimit),
				Type: command.ArgInt,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   listTriggers,
	}
}

func listTriggers(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	search := strings.TrimSpace(inv.String("search"))
	limit := clampTriggerLimit(inv, triggerListDefaultLimit, triggerListMaxLimit)

	req := pb.ListTriggersReq_builder{Limit: &limit}
	if search != "" {
		// Phrase only, not phrase AND reply: the server ANDs its filters, so
		// setting both would demand the same text appear in each.
		req.Phrase = &search
	}

	if mine := inv.Bool("mine"); mine {
		// A flag rather than the caller's user id, which the client does not
		// know: identity travels as a platform id in metadata, so asking for
		// "mine" by id meant a GetUser round trip first, purely to tell the
		// server something it resolves itself on every call.
		req.Mine = &mine
	}

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	resp, err := client.TriggerServiceClient.ListTriggers(callCtx, req.Build())
	if err != nil {
		logCallFailure("ListTriggers", err)
		return nil, err
	}

	triggers := resp.GetTriggers()
	if len(triggers) == 0 {
		return &command.Response{Content: "No triggers found.", Ephemeral: true}, nil
	}

	var b strings.Builder
	b.WriteString("**Triggers**\n")
	for _, t := range triggers {
		b.WriteString(renderTriggerLine(t))
		b.WriteByte('\n')
	}

	return &command.Response{Content: b.String(), Ephemeral: true}, nil
}

func triggerInfoCommand() command.Command {
	return command.Command{
		Name:        "triggerinfo",
		Description: "Show one trigger in detail",
		Group:       triggerGroup,
		Sub:         triggerSubInfo,
		Args: []command.Arg{
			{
				Name:        "id",
				Description: "Trigger id",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   triggerInfo,
	}
}

func triggerInfo(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	id := inv.String("id")

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	req := pb.GetTriggerReq_builder{Id: &id}.Build()
	resp, err := client.TriggerServiceClient.GetTrigger(callCtx, req)
	if err != nil {
		logCallFailure("GetTrigger", err)
		return nil, err
	}

	return &command.Response{
		Content:   formatTriggerInfo(resp.GetTrigger()),
		Ephemeral: true,
	}, nil
}

func triggerStatsCommand() command.Command {
	return command.Command{
		Name:        "triggerstats",
		Description: "Show the most-fired triggers on this server",
		Group:       triggerGroup,
		Sub:         triggerSubStats,
		Args: []command.Arg{
			{
				Name:        "kind",
				Description: kindArgDescription,
				Type:        command.ArgString,
			},
			{
				Name: "limit",
				Description: fmt.Sprintf("How many to show, up to %d (default %d)",
					triggerStatsMaxLimit, triggerStatsDefaultLimit),
				Type: command.ArgInt,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   triggerStats,
	}
}

func triggerStats(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	kind, ok := parseStatsKind(inv.String("kind"))
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument,
			"kind must be one of: %s", statsKindNames())
	}

	limit := clampTriggerLimit(inv, triggerStatsDefaultLimit, triggerStatsMaxLimit)

	instance, err := currentTriggerInstance(ctx)
	if err != nil {
		return nil, err
	}

	req := pb.GetTriggerStatsReq_builder{
		Instance:   instance,
		ActionType: &kind,
		Limit:      &limit,
	}.Build()

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	resp, err := client.TriggerServiceClient.GetTriggerStats(callCtx, req)
	if err != nil {
		logCallFailure("GetTriggerStats", err)
		return nil, err
	}

	return &command.Response{
		Content:   formatTriggerStats(resp.GetStats(), kind),
		Ephemeral: true,
	}, nil
}

func triggerExecCommand() command.Command {
	return command.Command{
		Name:        "triggerexec",
		Aliases:     []string{"triggerbyid"},
		Description: "Fire one trigger by id, ignoring its chance",
		Group:       triggerGroup,
		Sub:         triggerSubExec,
		Args: []command.Arg{
			{
				Name:        "id",
				Description: "Trigger id",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   execTrigger,
	}
}

// execTrigger is the one trigger command whose reply is NOT ephemeral: playing a
// trigger back is only useful if the channel sees it.
func execTrigger(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	id := inv.String("id")

	instance, err := currentTriggerInstance(ctx)
	if err != nil {
		return nil, err
	}

	req := pb.ExecTriggerReq_builder{
		Id:       &id,
		Instance: instance,
	}.Build()

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	resp, err := client.TriggerServiceClient.ExecTrigger(callCtx, req)
	if err != nil {
		logCallFailure("ExecTrigger", err)
		return nil, err
	}

	out, err := triggerPlaybackResponse(ctx, resp)
	if err != nil {
		return nil, err
	}
	if out == nil {
		// Should not happen: ExecTrigger answers NotFound for an id it cannot
		// fire. A non-nil response regardless, because respondCommand logs an
		// error and answers nothing for a nil one, which shows the caller "the
		// application did not respond". Ephemeral, unlike a real playback: there
		// is nothing for the channel to see.
		return &command.Response{
			Content:   "That trigger has nothing to play back.",
			Ephemeral: true,
		}, nil
	}

	return out, nil
}

// triggerPlaybackResponse renders a fired trigger, fetching the bytes when the
// reply is a file rather than text.
//
// nil, nil means nothing fired, which every caller reports its own way: the
// message path stays silent and execTrigger says so out loud.
func triggerPlaybackResponse(ctx context.Context, resp *pb.TryTriggerResp) (*command.Response, error) {
	switch {
	case resp.HasReply():
		return &command.Response{Content: resp.GetReply()}, nil

	case resp.HasFile():
		file := resp.GetFile()

		fileID := file.GetFileId()
		req := pb.GetFileReq_builder{FileId: &fileID}.Build()

		// Bounded: an 8 MiB inline response (ADR-0022) over a wedged connection
		// would otherwise hold this handler, and on the message path a goroutine
		// per message, open indefinitely.
		fileCtx, cancel := context.WithTimeout(ctx, triggerFileTimeout)
		defer cancel()

		got, err := client.TriggerServiceClient.GetFile(fileCtx, req)
		if err != nil {
			logCallFailure("GetFile", err)
			return nil, err
		}

		// An empty blob is treated as nothing fired. responseFiles drops a
		// zero-byte attachment, and Content is empty here by design, so sending
		// it would be a message with neither text nor a file — which Discord
		// rejects outright, leaving only a log line behind.
		if len(got.GetContent()) == 0 {
			log.Z.Warn("a trigger file has no content.", zap.String("file_id", fileID))
			return nil, nil
		}

		return triggerFileResponse(file, got.GetContent()), nil
	}

	return nil, nil
}

// triggerFileResponse builds a command.Response carrying a fired trigger's
// file as an attachment.
//
// Content is empty on purpose: the file is the reply, and a caption the author
// never wrote would be the bot talking over them.
func triggerFileResponse(file *pb.TriggerFile, content []byte) *command.Response {
	name := file.GetFilename()
	if name == "" {
		// The server derives a filename from the id and the sniffed type, so a
		// blank one means an older or hand-built response.
		name = file.GetFileId()
	}
	if name == "" {
		name = unnamedTriggerFileName
	}

	return &command.Response{
		File: &command.ResponseFile{
			Name:     name,
			MIMEType: file.GetMimeType(),
			Content:  content,
		},
	}
}

// formatTriggerInfo renders the detail view of one trigger.
//
// It is a pure function of the protobuf so it can be unit-tested without a
// Discord session or a gRPC client, and it is nil-safe throughout because
// discordgo does not recover a panic raised in a handler.
func formatTriggerInfo(t *pb.Trigger) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Trigger** `%s`\n", t.GetId())
	fmt.Fprintf(&b, "Phrase: %s\n", emptyDash(t.GetPhrase()))
	fmt.Fprintf(&b, "Mode: %s\n", triggerModeName(t.GetMode()))
	// The EFFECTIVE chance, never the stored one: a stored 0 means the default
	// and exact mode triples whatever it resolves to, so printing the column
	// would tell a user their trigger fires at 0% when it fires at 15%
	// (ADR-0021).
	fmt.Fprintf(&b, "Chance: %d%%\n", trigger.EffectiveChance(t.GetChance(), t.GetMode()))
	fmt.Fprintf(&b, "%s\n", triggerReplyLine(t))
	// renderReminderStamp is shared rather than duplicated: an absent instant
	// and a relative tag are the same problem here as on a reminder.
	fmt.Fprintf(&b, "Created: %s\n", renderReminderStamp(t.HasCreatedAt(), t.GetCreatedAt().AsTime()))
	fmt.Fprintf(&b, "Updated: %s", renderReminderStamp(t.HasUpdatedAt(), t.GetUpdatedAt().AsTime()))

	return b.String()
}

// triggerAnswer describes what a trigger actually answers with.
//
// TEXT WINS over a file, and that order is not cosmetic. chk_reply_or_file is an
// OR, not an exclusive one, so a row can hold both — and the server's
// buildTryTriggerResp checks the reply FIRST, so such a trigger fires with text
// and its file is never played. Showing the file here would tell a user their
// trigger does the opposite of what it does.
func triggerAnswer(t *pb.Trigger) (label string, value string, isFile bool) {
	if reply := t.GetReply(); reply != "" {
		return "Reply", reply, false
	}
	if file := t.GetFile(); file != nil {
		return "File", emptyDash(file.GetFilename()), true
	}

	return "Reply", emptyDash(""), false
}

// triggerReplyLine describes what a trigger answers with, for the detail view.
func triggerReplyLine(t *pb.Trigger) string {
	label, value, _ := triggerAnswer(t)

	return label + ": " + value
}

// renderTriggerLine renders one trigger for the list.
func renderTriggerLine(t *pb.Trigger) string {
	_, answer, isFile := triggerAnswer(t)
	if isFile {
		answer = "(file) " + answer
	}

	return fmt.Sprintf("`%s` — %s — %s — %d%% — %s",
		t.GetId(),
		emptyDash(t.GetPhrase()),
		triggerModeName(t.GetMode()),
		trigger.EffectiveChance(t.GetChance(), t.GetMode()),
		answer,
	)
}

// formatTriggerStats renders the leaderboard.
//
// An empty slice gets a sentence rather than a bare title: a heading with
// nothing under it reads like the reply was cut off.
func formatTriggerStats(stats []*pb.TriggerStat, kind pb.ActionType) string {
	label := statsKindLabel(kind)

	if len(stats) == 0 {
		return fmt.Sprintf("No %s recorded yet.", label)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**Top triggers by %s**\n", label)
	for i, stat := range stats {
		fmt.Fprintf(&b, "%d. %s — %d — %d%%\n",
			i+1,
			emptyDash(stat.GetPhrase()),
			stat.GetCount(),
			trigger.EffectiveChance(stat.GetChance(), stat.GetMode()),
		)
	}

	return strings.TrimRight(b.String(), "\n")
}

// currentTriggerInstance builds the pb.TriggerInstance for where the command
// was typed, using the canonical jsonb shape from callermeta so the server
// resolves the same instance row the interceptor would.
//
// FailedPrecondition, not Internal: a trigger command in a direct message has no
// guild to scope to, which is a user mistake rather than a bug, and errorMessage
// passes a FailedPrecondition message through to the caller verbatim.
//
// An EMPTY GuildID is refused as well as a missing origin, which currentDestination
// does not do. It is load-bearing on two counts: callermeta.NewOutgoingOrigin
// drops an origin with no instance uid, so the server would answer a DM with its
// own generic refusal after a round trip; and this is also the message path's
// gate, where that round trip would be paid once per direct message.
func currentTriggerInstance(ctx context.Context) (*pb.TriggerInstance, error) {
	origin, ok := originFromContext(ctx)
	if !ok || origin.GuildID == "" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"triggers belong to a server, so this has to be used in one")
	}

	meta := callermeta.Origin{InstanceUID: origin.GuildID, DestinationUID: origin.ChannelID}
	platform := pb.Platform_PLATFORM_DISCORD

	return pb.TriggerInstance_builder{
		PlatformEnum: &platform,
		InstanceMeta: meta.InstanceMeta(),
	}.Build(), nil
}

// triggerChance validates a supplied chance and returns it as a builder field,
// or nil when the caller did not supply one.
//
// nil rather than 0 is the whole point: 0 is a stored sentinel meaning "the
// default" (ADR-0021), so sending it for an omitted argument would overwrite a
// tuned chance on every edit.
func triggerChance(inv *command.Invocation) (*int32, error) {
	if !inv.Has("chance") {
		return nil, nil
	}

	raw := inv.Int("chance")
	if raw < 0 || raw > int64(trigger.MaxChance) {
		return nil, status.Errorf(codes.InvalidArgument,
			"chance must be between 0 and %d", trigger.MaxChance)
	}

	chance := int32(raw)

	return &chance, nil
}

// clampTriggerLimit resolves a caller-supplied limit. It clamps rather than
// refuses, because someone typing a huge number means "as many as you can".
func clampTriggerLimit(inv *command.Invocation, defaultLimit int64, maxLimit int64) int64 {
	if !inv.Has("limit") {
		return defaultLimit
	}

	limit := inv.Int("limit")
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}

	return limit
}

// invalidModeError names the accepted mode words, so a typo is answered with the
// list instead of a bare refusal.
func invalidModeError() error {
	return status.Errorf(codes.InvalidArgument, "mode must be one of: %s", triggerModeNames())
}

// explainRegexRefusal rewrites the server's PermissionDenied for a regex trigger
// into a FailedPrecondition naming the requirement.
//
// errorMessage maps PermissionDenied to a flat "You are not allowed to do that.",
// which for this one case hides a reason the user can act on, while it passes a
// FailedPrecondition message through verbatim. It only applies when the caller
// actually asked for regex mode: the server stays the sole authority on the
// decision, and any other refusal keeps its generic wording rather than being
// explained as something it may not be.
func explainRegexRefusal(err error, mode pb.TriggerMode) error {
	if mode != pb.TriggerMode_TRIGGER_MODE_REGEX || status.Code(err) != codes.PermissionDenied {
		return err
	}

	return status.Errorf(codes.FailedPrecondition,
		"regex triggers need %s clearance", regexClearanceRequirement)
}
