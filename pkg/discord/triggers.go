package discord

import (
	"bytes"
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
	"github.com/lasikuu/GinBot/pkg/trigger"
	"go.uber.org/zap"
)

const triggerGroup = "trigger"

const (
	triggerSubAdd   = "add"
	triggerSubDel   = "del"
	triggerSubMod   = "mod"
	triggerSubList  = "list"
	triggerSubInfo  = "info"
	triggerSubStats = "stats"
	triggerSubExec  = "exec"
)

const (
	triggerListDefaultLimit int64 = 15
	triggerListMaxLimit     int64 = 25

	triggerStatsDefaultLimit int64 = 10
	triggerStatsMaxLimit     int64 = 25
)

// triggerFileTimeout bounds the WHOLE GetFile stream, not one round trip. It
// must stay shorter than triggerAttemptTimeout, or nesting makes it a no-op.
const triggerFileTimeout = 10 * time.Second

// maxTriggerFileBytes mirrors pkg/storage.MaxFileBytes by value; a platform
// client must not link that package.
const maxTriggerFileBytes = 8 << 20

// triggerCallTimeout bounds every other trigger RPC; handler contexts have no
// deadline of their own.
const triggerCallTimeout = 20 * time.Second

// unnamedTriggerFileName is a last resort: Discord rejects a blank filename.
const unnamedTriggerFileName = "trigger"

// Derived from the enum so it cannot drift from the server's clearance floor.
var regexClearanceRequirement = strings.ToLower(strings.TrimPrefix(
	pb.Clearance_CLEARANCE_MODERATOR.String(), "CLEARANCE_"))

// A URL, not an attachment: the server fetches it from an allow-listed CDN
// host (ADR-0007).
const fileArgDescription = "Discord CDN link to reply with, e.g. copied from an existing attachment"

var modeArgDescription = "How the phrase is matched: " + triggerModeNames() + " (default any)"

var chanceArgDescription = fmt.Sprintf(
	"Percent chance of firing, 0-%d. 0 means the default, which exact mode triples",
	trigger.MaxChance,
)

var kindArgDescription = "Which counter to rank by: " + statsKindNames()

// A slice for a stable order in the accepted-values message.
var triggerModeWords = []struct {
	word string
	mode pb.TriggerMode
}{
	{"exact", pb.TriggerMode_TRIGGER_MODE_EXACT},
	{"any", pb.TriggerMode_TRIGGER_MODE_ANY},
	{"regex", pb.TriggerMode_TRIGGER_MODE_REGEX},
}

var statsKindWords = []struct {
	word       string
	label      string
	actionType pb.ActionType
}{
	{"occurred", "random fires", pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED},
	{"called", "forced fires", pb.ActionType_ACTION_TYPE_TRIGGER_CALLED},
}

func boundedCall(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, triggerCallTimeout)
}

// NotFound is the ordinary answer for a mistyped id, so it stays at debug.
func logCallFailure(rpc string, err error) {
	if connect.CodeOf(err) == connect.CodeNotFound {
		log.Z.Debug("trigger rpc found nothing.", zap.String("rpc", rpc))
		return
	}

	// Never the phrase, reply or id: stored content must not reach a log line.
	log.Z.Error("trigger rpc failed.", zap.String("rpc", rpc), zap.Error(err))
}

// An empty value yields UNSPECIFIED with ok true, for the server to default.
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

func triggerModeNames() string {
	words := make([]string, 0, len(triggerModeWords))
	for _, candidate := range triggerModeWords {
		words = append(words, candidate.word)
	}

	return strings.Join(words, ", ")
}

func triggerModeName(mode pb.TriggerMode) string {
	return strings.ToLower(strings.TrimPrefix(mode.String(), "TRIGGER_MODE_"))
}

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

func statsKindNames() string {
	words := make([]string, 0, len(statsKindWords))
	for _, candidate := range statsKindWords {
		words = append(words, candidate.word)
	}

	return strings.Join(words, ", ")
}

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
		Name:        "triggeradd",
		Aliases:     []string{"triggercreate"},
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
		// The server's CDN fetch outlasts Discord's 3s interaction deadline.
		Slow:    true,
		Handler: addTrigger,
	}
}

func addTrigger(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	phrase := inv.String("phrase")
	reply := strings.TrimSpace(inv.String("reply"))
	fileURL := strings.TrimSpace(inv.String("file"))

	if reply == "" && fileURL == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a trigger needs either a reply or a file"))
	}
	// The server fires the reply when both are set, so the file could never play.
	if reply != "" && fileURL != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a trigger answers with a reply or a file, not both"))
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
	if mode != pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED {
		b.Mode = &mode
	}
	if chance != nil {
		b.Chance = chance
	}

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	resp, err := clientsFrom(ctx).Trigger.CreateTrigger(callCtx, connect.NewRequest(b.Build()))
	if err != nil {
		logCallFailure("CreateTrigger", err)
		return nil, explainRegexRefusal(err, mode)
	}

	return &command.Response{
		Content:   fmt.Sprintf("Trigger created: `%s`.", resp.Msg.GetId()),
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

// No instance: the server scopes ownership itself and answers NotFound by
// design, so a caller cannot probe for ids.
func deleteTrigger(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	id := inv.String("id")

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	req := pb.DeleteTriggerReq_builder{Id: &id}.Build()
	if _, err := clientsFrom(ctx).Trigger.DeleteTrigger(callCtx, connect.NewRequest(req)); err != nil {
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
		// Slow for addTrigger's reason: a new file argument is fetched first.
		Slow:    true,
		Handler: modifyTrigger,
	}
}

// UpdateTriggerReq is patch-shaped: the server branches on HasX(), so an unset
// field means "leave it alone".
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

	// Chat arguments bind positionally, so a blank is a placeholder for a later
	// argument rather than a request to clear.
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

	// The server would accept an id-only update, do nothing and report success.
	if b.Phrase == nil && b.Reply == nil && b.FileUrl == nil && b.Chance == nil && b.Mode == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("nothing to change: give at least one of phrase, reply, file, chance or mode"))
	}

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	if _, err := clientsFrom(ctx).Trigger.UpdateTrigger(callCtx, connect.NewRequest(b.Build())); err != nil {
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
		// Phrase only: the server ANDs its filters.
		req.Phrase = &search
	}

	if mine := inv.Bool("mine"); mine {
		// A flag, not a user id: caller identity travels in request headers.
		req.Mine = &mine
	}

	callCtx, cancel := boundedCall(ctx)
	defer cancel()

	resp, err := clientsFrom(ctx).Trigger.ListTriggers(callCtx, connect.NewRequest(req.Build()))
	if err != nil {
		logCallFailure("ListTriggers", err)
		return nil, err
	}

	triggers := resp.Msg.GetTriggers()
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
	resp, err := clientsFrom(ctx).Trigger.GetTrigger(callCtx, connect.NewRequest(req))
	if err != nil {
		logCallFailure("GetTrigger", err)
		return nil, err
	}

	return &command.Response{
		Content:   formatTriggerInfo(resp.Msg.GetTrigger()),
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
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("kind must be one of: %s", statsKindNames()))
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

	resp, err := clientsFrom(ctx).Trigger.GetTriggerStats(callCtx, connect.NewRequest(req))
	if err != nil {
		logCallFailure("GetTriggerStats", err)
		return nil, err
	}

	return &command.Response{
		Content:   formatTriggerStats(resp.Msg.GetStats(), kind),
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

// The one trigger command whose reply is not ephemeral: the channel must see it.
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

	resp, err := clientsFrom(ctx).Trigger.ExecTrigger(callCtx, connect.NewRequest(req))
	if err != nil {
		logCallFailure("ExecTrigger", err)
		return nil, err
	}

	// callCtx, not ctx: ctx is unbounded, so triggerFileTimeout would be a no-op.
	out, err := triggerPlaybackResponse(callCtx, resp.Msg)
	if err != nil {
		return nil, err
	}
	if out == nil {
		// A nil response makes Discord show "the application did not respond".
		return &command.Response{
			Content:   "That trigger has nothing to play back.",
			Ephemeral: true,
		}, nil
	}

	return out, nil
}

// A nil, nil return means nothing fired.
func triggerPlaybackResponse(ctx context.Context, resp *pb.TryTriggerResp) (*command.Response, error) {
	switch {
	case resp.HasReply():
		return &command.Response{Content: resp.GetReply()}, nil

	case resp.HasFile():
		file := resp.GetFile()

		fileID := file.GetFileId()
		req := pb.GetFileReq_builder{FileId: &fileID}.Build()

		fileCtx, cancel := context.WithTimeout(ctx, triggerFileTimeout)
		defer cancel()

		stream, err := clientsFrom(ctx).Trigger.GetFile(fileCtx, connect.NewRequest(req))
		if err != nil {
			logCallFailure("GetFile", err)
			return nil, err
		}
		defer func() {
			_ = stream.Close()
		}()

		var meta *pb.TriggerFile
		var content bytes.Buffer

		for stream.Receive() {
			chunk := stream.Msg()

			// An unset, unrecognised or duplicate arm is ordinary input from an
			// untrusted server, not a bug.
			switch {
			case chunk.HasMeta():
				if meta == nil {
					meta = chunk.GetMeta().GetFile()
				}
			case chunk.HasContent():
				if content.Len()+len(chunk.GetContent()) > maxTriggerFileBytes {
					log.Z.Error("trigger file exceeds the client's own size cap; aborting playback.",
						zap.String("file_id", fileID))
					return nil, connect.NewError(connect.CodeInternal, errors.New("trigger file exceeds the maximum size"))
				}
				content.Write(chunk.GetContent())
			}
		}
		if err := stream.Err(); err != nil {
			logCallFailure("GetFile", err)
			return nil, err
		}

		// Discord rejects a message carrying neither text nor a file.
		if content.Len() == 0 {
			log.Z.Warn("a trigger file has no content.", zap.String("file_id", fileID))
			return nil, nil
		}

		// A truncated file yields a clean EOF with a nil stream.Err(), so the
		// declared byte_size is the only way to catch a short stream.
		if meta != nil && meta.GetByteSize() > 0 && int64(content.Len()) != meta.GetByteSize() {
			log.Z.Error("trigger file stream ended short of its declared size.",
				zap.String("file_id", fileID),
				zap.Int("received_bytes", content.Len()),
				zap.Int64("declared_bytes", meta.GetByteSize()),
			)
			return nil, connect.NewError(connect.CodeInternal, errors.New("trigger file was incomplete"))
		}

		displayFile := file
		if meta != nil {
			displayFile = meta
		}

		return triggerFileResponse(displayFile, content.Bytes()), nil
	}

	return nil, nil
}

// Content is empty on purpose: the file is the reply.
func triggerFileResponse(file *pb.TriggerFile, content []byte) *command.Response {
	name := file.GetFilename()
	if name == "" {
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

// Nil-safe throughout: discordgo does not recover a panic raised in a handler.
func formatTriggerInfo(t *pb.Trigger) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Trigger** `%s`\n", t.GetId())
	fmt.Fprintf(&b, "Phrase: %s\n", emptyDash(t.GetPhrase()))
	fmt.Fprintf(&b, "Mode: %s\n", triggerModeName(t.GetMode()))
	// The effective chance, never the stored column: 0 means default (ADR-0021).
	fmt.Fprintf(&b, "Chance: %d%%\n", trigger.EffectiveChance(t.GetChance(), t.GetMode()))
	fmt.Fprintf(&b, "%s\n", triggerReplyLine(t))
	fmt.Fprintf(&b, "Created: %s\n", renderReminderStamp(t.HasCreatedAt(), t.GetCreatedAt().AsTime()))
	fmt.Fprintf(&b, "Updated: %s", renderReminderStamp(t.HasUpdatedAt(), t.GetUpdatedAt().AsTime()))

	return b.String()
}

// Text wins over a file: the server checks the reply first when a row has both.
func triggerAnswer(t *pb.Trigger) (label string, value string, isFile bool) {
	if reply := t.GetReply(); reply != "" {
		return "Reply", reply, false
	}
	if file := t.GetFile(); file != nil {
		return "File", emptyDash(file.GetFilename()), true
	}

	return "Reply", emptyDash(""), false
}

func triggerReplyLine(t *pb.Trigger) string {
	label, value, _ := triggerAnswer(t)

	return label + ": " + value
}

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

// An empty GuildID is refused here so a DM costs no round trip;
// FailedPrecondition is the one code errorMessage passes through verbatim.
func currentTriggerInstance(ctx context.Context) (*pb.TriggerInstance, error) {
	origin, ok := originFromContext(ctx)
	if !ok || origin.GuildID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("triggers belong to a server, so this has to be used in one"))
	}

	meta := callermeta.Origin{InstanceUID: origin.GuildID, DestinationUID: origin.ChannelID}
	platform := pb.Platform_PLATFORM_DISCORD

	return pb.TriggerInstance_builder{
		PlatformEnum: &platform,
		InstanceMeta: meta.InstanceMeta(),
	}.Build(), nil
}

// nil, not 0, for an omitted argument: 0 is the stored "default" sentinel and
// would overwrite a tuned chance (ADR-0021).
func triggerChance(inv *command.Invocation) (*int32, error) {
	if !inv.Has("chance") {
		return nil, nil
	}

	raw := inv.Int("chance")
	if raw < 0 || raw > int64(trigger.MaxChance) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("chance must be between 0 and %d", trigger.MaxChance))
	}

	chance := int32(raw)

	return &chance, nil
}

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

func invalidModeError() error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("mode must be one of: %s", triggerModeNames()))
}

// PermissionDenied is flattened by errorMessage; FailedPrecondition survives.
func explainRegexRefusal(err error, mode pb.TriggerMode) error {
	if mode != pb.TriggerMode_TRIGGER_MODE_REGEX || connect.CodeOf(err) != connect.CodePermissionDenied {
		return err
	}

	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("regex triggers need %s clearance", regexClearanceRequirement))
}
