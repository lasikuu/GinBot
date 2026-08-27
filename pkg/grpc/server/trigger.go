package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/storage"
	"github.com/lasikuu/GinBot/pkg/trigger"
	"go.uber.org/zap"
)

// regexClearanceFloor is the minimum clearance required to create or update a
// TRIGGER_MODE_REGEX trigger: a regex runs against every message on the
// instances it is scoped to, so it is not an ordinary user action the way an
// exact- or any-mode trigger is. interceptor.DefaultRequirements keeps the
// per-RPC floor at CLEARANCE_REGISTERED; this is an in-handler check on top.
const regexClearanceFloor = pb.Clearance_CLEARANCE_MODERATOR

// triggerFileKeyPrefix namespaces trigger media within the shared blob store.
const triggerFileKeyPrefix = "trigger/"

// mimeExtensions maps a sniffed, allow-listed MIME type onto a filename
// extension. file.path stores no original name, so this is how GetFile and
// every trigger reply derive one for display and for the attachment name on
// playback.
var mimeExtensions = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"video/mp4":  ".mp4",
	"video/webm": ".webm",
	"audio/mpeg": ".mp3",
	"audio/ogg":  ".ogg",
	"audio/wave": ".wav",
}

// TriggerServer implements TriggerService.
type TriggerServer struct {
	ginbotv1connect.UnimplementedTriggerServiceHandler

	cache   *trigger.Cache
	limiter *trigger.ForcedLimiter
	fetcher *storage.Fetcher
	blobs   storage.Storage
	roll    trigger.Roller
}

// NewTriggerServer returns a TriggerServer wired to the package-level storage
// and a fetcher restricted to the platform CDN hosts.
func NewTriggerServer() *TriggerServer {
	return newTriggerServer(
		storage.NewFetcher(nil, storage.DefaultAllowedHosts(), storage.MaxFileBytes),
		storage.Default(),
	)
}

// newTriggerServer builds a TriggerServer over an explicit fetcher and blob
// store.
//
// It exists as a seam: the media path is only reachable through the CDN
// allow-list, so without a way to substitute both the fetcher and the store the
// fetch-store-dedupe-play-back path could not be exercised without a live
// external dependency. Production goes through NewTriggerServer.
func newTriggerServer(fetcher *storage.Fetcher, blobs storage.Storage) *TriggerServer {
	s := &TriggerServer{
		limiter: trigger.NewForcedLimiter(nil),
		fetcher: fetcher,
		blobs:   blobs,
		// This is a chance roll, not a security decision: math/rand/v2's
		// package-level source is auto-seeded and safe for concurrent use,
		// which is what the cache and every handler sharing this server need.
		roll: rand.IntN,
	}
	s.cache = trigger.NewCache(s.loadCandidates)

	return s
}

// PruneForcedLimiter drops forced-fire records that can no longer refuse
// anything.
//
// trigger.ForcedLimiter keeps one entry per author and documents that Allow does
// not prune, so that it stays O(1) — pruning is the caller's job and nothing was
// doing it. That was harmless only while no client ever set forced; the Discord
// client now sets it on every mention, so the map would otherwise grow by one
// entry per distinct author for the lifetime of the process.
func (s *TriggerServer) PruneForcedLimiter() {
	s.limiter.Prune()
}

// loadCandidates is the trigger.Loader backing s.cache: it fetches every live
// trigger scoped to an instance and compiles each one's pattern once, here,
// rather than per message.
func (s *TriggerServer) loadCandidates(ctx context.Context, instanceID int64) ([]trigger.Candidate, error) {
	actives, err := db.ListActiveTriggersByInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	candidates := make([]trigger.Candidate, 0, len(actives))
	for _, active := range actives {
		mode := pb.TriggerMode(active.Mode)
		compiled, compileErr := trigger.Compile(active.Phrase, mode)
		if compileErr != nil {
			// No zap.Error here: a regexp compile error embeds the offending
			// pattern text, and a stored phrase must never be echoed into logs.
			log.Z.Warn("skipping trigger whose pattern no longer compiles",
				zap.String("trigger_id", active.ID))
			continue
		}

		candidates = append(candidates, trigger.Candidate{
			ID:      active.ID,
			Mode:    mode,
			Chance:  active.Chance,
			Pattern: compiled,
		})
	}

	return candidates, nil
}

// resolveInstance maps a *pb.TriggerInstance onto its instance row's id.
//
// codes.NotFound rather than a get-or-create: interceptor.NewOriginInterceptor
// already bootstraps the instance for a call's own origin on every
// authenticated RPC, so by the time a handler runs, any instance the caller
// could legitimately name already exists.
func resolveInstance(ctx context.Context, instance *pb.TriggerInstance) (int64, error) {
	if instance == nil || !instance.HasPlatformEnum() || !instance.HasInstanceMeta() {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance is required"))
	}

	row, err := db.GetInstanceByMeta(ctx, instance.GetPlatformEnum(), instance.GetInstanceMeta())
	if errors.Is(err, db.ErrNotFound) {
		return 0, connect.NewError(connect.CodeNotFound, fmt.Errorf("instance not found"))
	}
	if err != nil {
		log.Z.Error("failed to resolve trigger instance", zap.Error(err))
		return 0, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve instance"))
	}

	return row.ID, nil
}

// callerOriginInstanceID resolves the instance id for the call's own origin —
// the same instance interceptor.NewOriginInterceptor already bootstrapped
// for this request. ok is false for a call with no origin (e.g. a direct
// message) or whose origin cannot be resolved.
func callerOriginInstanceID(ctx context.Context) (int64, bool) {
	origin, ok := interceptor.OriginFromContext(ctx)
	if !ok {
		return 0, false
	}

	meta, err := getMetadata(ctx)
	if err != nil {
		return 0, false
	}

	row, err := db.GetInstanceByMeta(ctx, meta.PlatformEnum, origin.InstanceMeta())
	if err != nil {
		return 0, false
	}

	return row.ID, true
}

// callerScopedInstance resolves the single instance an RPC is allowed to act
// on, and refuses a request that names any other one.
//
// A request may carry an instance, but instance_meta is only the platform's own
// guild or room identifier, which is public. Trusting it unchecked would let any
// registered caller read, fire or plant triggers in a guild they have nothing to
// do with, so the named instance must be the one the call actually came from.
// NotFound rather than PermissionDenied, so the error cannot be used to discover
// which guilds the bot is in.
//
// A nil requested instance falls back to the origin, which is what a caller that
// simply did not name one means.
func callerScopedInstance(ctx context.Context, requested *pb.TriggerInstance) (int64, error) {
	originID, ok := callerOriginInstanceID(ctx)
	if !ok {
		return 0, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this action must be used in a server or room"))
	}

	if requested == nil {
		return originID, nil
	}

	requestedID, err := resolveInstance(ctx, requested)
	if err != nil {
		return 0, err
	}
	if requestedID != originID {
		return 0, connect.NewError(connect.CodeNotFound, fmt.Errorf("instance not found"))
	}

	return originID, nil
}

// displayFilename derives a display name for a stored file: the original
// name is never kept, so this is the file id plus the extension its sniffed
// MIME type implies.
func displayFilename(file *model.File) string {
	return file.ID + mimeExtensions[file.MimeType]
}

// invalidPatternError maps a trigger.Compile failure to InvalidArgument
// without echoing the submitted phrase back — a regexp compile error embeds
// the offending pattern text, so the message names which rule was broken
// instead of repeating it.
func invalidPatternError(err error) error {
	switch {
	case errors.Is(err, trigger.ErrEmptyPhrase):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("phrase must not be blank"))
	case errors.Is(err, trigger.ErrPatternTooLong):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("phrase exceeds the maximum length of %d characters", trigger.MaxPatternLength))
	default:
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("phrase is not a valid pattern for the selected mode"))
	}
}

// mapFetchError maps a storage.Fetcher failure to the gRPC status a caller
// should see: refusals of the caller's own input are InvalidArgument, and
// anything else is the server breaking.
func mapFetchError(err error) error {
	switch {
	case errors.Is(err, storage.ErrHostNotAllowed):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file_url host is not allowed"))
	case errors.Is(err, storage.ErrTooLarge):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file_url content exceeds the maximum size of %d bytes", storage.MaxFileBytes))
	case errors.Is(err, storage.ErrUnsupportedType):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file_url content type is not supported"))
	default:
		log.Z.Error("failed to fetch trigger file", zap.Error(err))
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to fetch file"))
	}
}

// fetchAndStoreFile downloads fileURL and resolves it to a file row id,
// writing the blob through storage only when the row is new — that is the
// content-hash dedupe.
func (s *TriggerServer) fetchAndStoreFile(ctx context.Context, fileURL string) (string, error) {
	fetched, err := s.fetcher.Fetch(ctx, fileURL)
	if err != nil {
		return "", mapFetchError(err)
	}

	// Fan-out by the first two hex characters of the hash, per pkg/storage's
	// documented key layout, so one flat directory does not grow without
	// bound.
	key := triggerFileKeyPrefix + fetched.Hash[:2] + "/" + fetched.Hash

	fileID, inserted, err := db.GetOrCreateFileByHash(ctx, fetched.Hash, key, fetched.MIMEType, int32(len(fetched.Content)))
	if err != nil {
		log.Z.Error("failed to resolve trigger file row", zap.Error(err))
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("failed to store file"))
	}

	if !inserted {
		// Identical bytes already have a row and a blob: this is the dedupe.
		return fileID, nil
	}

	if s.blobs == nil {
		log.Z.Error("trigger file storage is not configured")
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("failed to store file"))
	}
	if _, err := s.blobs.Put(ctx, key, bytes.NewReader(fetched.Content)); err != nil {
		log.Z.Error("failed to write trigger file blob", zap.Error(err))
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("failed to store file"))
	}

	return fileID, nil
}

// resolveScopeInstances turns a caller-supplied instance list into instance
// ids, falling back to the caller's own call origin when none was supplied.
//
// Every named instance must be the call's own origin. Scoping a new trigger to
// an arbitrary instance would let any registered user plant an auto-responder
// in a guild they have nothing to do with, so a list naming anything else is
// refused rather than filtered.
func resolveScopeInstances(ctx context.Context, requested []*pb.TriggerInstance) ([]int64, error) {
	if len(requested) == 0 {
		instanceID, err := callerScopedInstance(ctx, nil)
		if err != nil {
			return nil, err
		}
		return []int64{instanceID}, nil
	}

	ids := make([]int64, 0, len(requested))
	for _, instance := range requested {
		instanceID, err := callerScopedInstance(ctx, instance)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, instanceID) {
			ids = append(ids, instanceID)
		}
	}

	return ids, nil
}

// buildTriggerFile loads a file row and builds the protobuf TriggerFile for
// it, deriving the display filename since the original name is not stored.
func (s *TriggerServer) buildTriggerFile(ctx context.Context, fileID string) (*pb.TriggerFile, error) {
	fileRow, err := db.GetFile(ctx, fileID)
	if errors.Is(err, db.ErrNotFound) {
		// trigger.file_id is a foreign key: a missing row here means the
		// referenced file was hard-deleted out of band, not a caller mistake.
		log.Z.Error("trigger references a missing file row", zap.String("file_id", fileID))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load trigger file"))
	}
	if err != nil {
		log.Z.Error("failed to load trigger file", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load trigger file"))
	}

	return fileRow.ToProto(displayFilename(fileRow)), nil
}

// buildTryTriggerResp loads the fired trigger's row and builds the reply or
// file response for it.
func (s *TriggerServer) buildTryTriggerResp(ctx context.Context, triggerID string) (*pb.TryTriggerResp, error) {
	row, err := db.GetTrigger(ctx, triggerID)
	if errors.Is(err, db.ErrNotFound) {
		// The candidate came from the cache; the row was deleted between the
		// cache load and now. Nothing to reply with, and not the caller's fault.
		return pb.TryTriggerResp_builder{}.Build(), nil
	}
	if err != nil {
		log.Z.Error("failed to load selected trigger", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load trigger"))
	}

	id := row.ID
	builder := pb.TryTriggerResp_builder{Id: &id}

	switch {
	case row.Reply != nil && *row.Reply != "":
		builder.Reply = row.Reply
	case row.FileID != nil && *row.FileID != "":
		file, fileErr := s.buildTriggerFile(ctx, *row.FileID)
		if fileErr != nil {
			return nil, fileErr
		}
		builder.File = file
	default:
		// Impossible per chk_reply_or_file, but a handler must not return a
		// malformed response for a row it cannot fully explain.
		log.Z.Error("trigger row has neither a reply nor a file", zap.String("trigger_id", id))
		return pb.TryTriggerResp_builder{}.Build(), nil
	}

	return builder.Build(), nil
}

// TryTrigger evaluates message-matching against an instance's compiled set
// and, on a fire, records the outcome. It never queries the database when
// nothing matches: candidates come from the cache, and only a match's own row
// is fetched afterwards.
func (s *TriggerServer) TryTrigger(ctx context.Context, connReq *connect.Request[pb.TryTriggerReq]) (*connect.Response[pb.TryTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasInstance() || !req.GetInstance().HasPlatformEnum() || !req.GetInstance().HasInstanceMeta() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance is required"))
	}
	if req.GetPhrase() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("phrase is required"))
	}

	instanceID, err := callerScopedInstance(ctx, req.GetInstance())
	if err != nil {
		return nil, err
	}

	candidates, err := s.cache.Candidates(ctx, instanceID)
	if err != nil {
		log.Z.Error("failed to load trigger candidates", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to evaluate triggers"))
	}

	selected := trigger.Select(req.GetPhrase(), candidates, s.roll)
	if selected == nil {
		// A non-match is not an error.
		return connect.NewResponse(pb.TryTriggerResp_builder{}.Build()), nil
	}

	actionType := pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED
	if req.GetForced() {
		if !s.limiter.Allow(caller.ID) {
			// Rate limited is not an error either; the client simply says
			// nothing.
			return connect.NewResponse(pb.TryTriggerResp_builder{}.Build()), nil
		}
		actionType = pb.ActionType_ACTION_TYPE_TRIGGER_CALLED
	} else if !trigger.Fires(*selected, s.roll) {
		return connect.NewResponse(pb.TryTriggerResp_builder{}.Build()), nil
	}

	resp, err := s.buildTryTriggerResp(ctx, selected.ID)
	if err != nil {
		return nil, err
	}

	// Recording the action must not fail the RPC: a dropped statistic is
	// better than a swallowed trigger.
	if err := db.RecordTriggerFire(ctx, actionType, selected.ID, caller.ID); err != nil {
		log.Z.Error("failed to record trigger fire", zap.Error(err))
	}

	return connect.NewResponse(resp), nil
}

// ExecTrigger fires a specific trigger unconditionally, provided it is scoped
// to the given instance. No chance roll: an explicit execution always fires.
// Not rate limited: it is an explicit command, and any cooldown belongs at the
// command layer.
func (s *TriggerServer) ExecTrigger(ctx context.Context, connReq *connect.Request[pb.ExecTriggerReq]) (*connect.Response[pb.TryTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	if !req.HasInstance() || !req.GetInstance().HasPlatformEnum() || !req.GetInstance().HasInstanceMeta() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance is required"))
	}

	instanceID, err := callerScopedInstance(ctx, req.GetInstance())
	if err != nil {
		return nil, err
	}

	row, err := db.GetTrigger(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}
	if err != nil {
		log.Z.Error("failed to load trigger for exec", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load trigger"))
	}

	scopedIDs, err := db.ListTriggerInstanceIDs(ctx, row.ID)
	if err != nil {
		log.Z.Error("failed to load trigger scope", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load trigger"))
	}
	if !slices.Contains(scopedIDs, instanceID) {
		// NotFound rather than PermissionDenied: a caller must not be able to
		// fire another guild's trigger into theirs, and must not learn that
		// the id exists elsewhere.
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}

	resp, err := s.buildTryTriggerResp(ctx, row.ID)
	if err != nil {
		return nil, err
	}

	if err := db.RecordTriggerFire(ctx, pb.ActionType_ACTION_TYPE_TRIGGER_CALLED, row.ID, caller.ID); err != nil {
		log.Z.Error("failed to record trigger fire", zap.Error(err))
	}

	return connect.NewResponse(resp), nil
}

// GetTrigger returns a trigger with its file and instances. A trigger is
// readable by its creator, and by any caller on an instance it is scoped to
// — a trigger that can fire in your guild is not a secret from you. Anyone
// else gets NotFound, so the response does not confirm the id exists.
func (s *TriggerServer) GetTrigger(ctx context.Context, connReq *connect.Request[pb.GetTriggerReq]) (*connect.Response[pb.GetTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	row, err := db.GetTrigger(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}
	if err != nil {
		log.Z.Error("failed to get trigger", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get trigger"))
	}

	scopedIDs, err := db.ListTriggerInstanceIDs(ctx, row.ID)
	if err != nil {
		log.Z.Error("failed to load trigger scope", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get trigger"))
	}

	visible := row.UserID != nil && *row.UserID == caller.ID
	if !visible {
		if originInstanceID, ok := callerOriginInstanceID(ctx); ok {
			visible = slices.Contains(scopedIDs, originInstanceID)
		}
	}
	if !visible {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}

	var file *pb.TriggerFile
	if row.FileID != nil && *row.FileID != "" {
		file, err = s.buildTriggerFile(ctx, *row.FileID)
		if err != nil {
			return nil, err
		}
	}

	instances, err := db.GetTriggerInstances(ctx, row.ID)
	if err != nil {
		log.Z.Error("failed to load trigger instances", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get trigger"))
	}

	return connect.NewResponse(pb.GetTriggerResp_builder{
		Trigger: row.ToProto(file, instances),
	}.Build()), nil
}

// ListTriggers is scoped to the caller's own call origin instance, and narrows
// further to the caller's own triggers when `mine` is set. There is no way to
// ask for another user's: the owner predicate can only ever be filled from the
// resolved caller, which is why the request carries a boolean rather than a user
// id.
//
// A call with no resolvable origin instance, such as a direct message, falls
// back to the caller's own triggers. It must not fall through unscoped: an
// unset instance and an unset user id together would make db.ListTriggers skip
// both predicates and return every trigger in the database, across every guild.
// The else branch below is what prevents that, and it does not depend on `mine`.
func (s *TriggerServer) ListTriggers(ctx context.Context, connReq *connect.Request[pb.ListTriggersReq]) (*connect.Response[pb.ListTriggersResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	filter := db.ListTriggersFilter{
		PhraseSearch: req.GetPhrase(),
		ReplySearch:  req.GetReply(),
		Limit:        req.GetLimit(),
		Offset:       req.GetOffset(),
	}
	if req.GetMine() {
		filter.UserID = caller.ID
	}
	if req.HasMode() {
		modeValue := int32(req.GetMode().Number())
		filter.Mode = &modeValue
	}
	if req.HasPeriodStart() {
		start := req.GetPeriodStart().AsTime()
		filter.PeriodStart = &start
	}
	if req.HasPeriodEnd() {
		end := req.GetPeriodEnd().AsTime()
		filter.PeriodEnd = &end
	}
	if instanceID, ok := callerOriginInstanceID(ctx); ok {
		filter.InstanceID = instanceID
	} else {
		filter.UserID = caller.ID
	}

	rows, err := db.ListTriggers(ctx, filter)
	if err != nil {
		log.Z.Error("failed to list triggers", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list triggers"))
	}

	out := make([]*pb.Trigger, 0, len(rows))
	for _, row := range rows {
		var file *pb.TriggerFile
		if row.FileID != nil && *row.FileID != "" {
			file, err = s.buildTriggerFile(ctx, *row.FileID)
			if err != nil {
				return nil, err
			}
		}

		instances, instErr := db.GetTriggerInstances(ctx, row.ID)
		if instErr != nil {
			log.Z.Error("failed to load trigger instances", zap.Error(instErr))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list triggers"))
		}

		out = append(out, row.ToProto(file, instances))
	}

	return connect.NewResponse(pb.ListTriggersResp_builder{
		Triggers: out,
	}.Build()), nil
}

// CreateTrigger validates the pattern and the chance at creation time, never
// at match time, gates TRIGGER_MODE_REGEX behind regexClearanceFloor, fetches
// and dedupes a file_url when given one, and scopes the trigger to the
// requested instances or, absent those, to the caller's own call origin.
func (s *TriggerServer) CreateTrigger(ctx context.Context, connReq *connect.Request[pb.CreateTriggerReq]) (*connect.Response[pb.CreateTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasPhrase() || req.GetPhrase() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("phrase is required"))
	}
	if req.GetReply() == "" && req.GetFileUrl() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reply or file_url is required"))
	}

	mode := pb.TriggerMode_TRIGGER_MODE_ANY
	if req.HasMode() && req.GetMode() != pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED {
		mode = req.GetMode()
	}
	if mode == pb.TriggerMode_TRIGGER_MODE_REGEX && caller.Clearance < int32(regexClearanceFloor) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("regex triggers require %s clearance", regexClearanceFloor.String()))
	}
	if _, err := trigger.Compile(req.GetPhrase(), mode); err != nil {
		return nil, invalidPatternError(err)
	}

	chance := req.GetChance()
	if chance < 0 || chance > trigger.MaxChance {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chance must be between 0 and %d", trigger.MaxChance))
	}

	var fileID string
	if req.GetFileUrl() != "" {
		fileID, err = s.fetchAndStoreFile(ctx, req.GetFileUrl())
		if err != nil {
			return nil, err
		}
	}

	instanceIDs, err := resolveScopeInstances(ctx, req.GetInstances())
	if err != nil {
		return nil, err
	}

	triggerID, err := db.CreateTrigger(ctx, db.CreateTriggerParams{
		Phrase:      req.GetPhrase(),
		Reply:       req.GetReply(),
		FileID:      fileID,
		UserID:      caller.ID,
		Chance:      chance,
		Mode:        mode,
		InstanceIDs: instanceIDs,
	})
	if errors.Is(err, db.ErrExactPhraseTaken) {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("an exact trigger with this phrase already exists"))
	}
	if err != nil {
		// If the trigger insert fails after a file_url blob was written, that
		// blob is left behind for the GC job to collect rather than being
		// compensation-deleted here: a compensating delete could remove a blob
		// another trigger deduped onto.
		log.Z.Error("failed to create trigger", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create trigger"))
	}

	for _, instanceID := range instanceIDs {
		s.cache.Invalidate(instanceID)
	}

	return connect.NewResponse(pb.CreateTriggerResp_builder{
		Id: &triggerID,
	}.Build()), nil
}

// UpdateTrigger applies the same validation CreateTrigger does to any field
// being changed. Ownership is enforced in SQL by db.UpdateTriggerByUser, but a
// phrase or mode change is validated here first — which needs the trigger's
// CURRENT phrase and mode too, since editing only one of the pair must still
// be checked against the combination that will actually be stored, not an
// assumed default that could let a broken regex-mode phrase through
// unvalidated.
func (s *TriggerServer) UpdateTrigger(ctx context.Context, connReq *connect.Request[pb.UpdateTriggerReq]) (*connect.Response[pb.UpdateTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	// Ownership is resolved first, before anything expensive or observable
	// happens. Deferring it until after the file fetch would let a caller who
	// owns nothing name any id plus a file_url and still cause a CDN download, a
	// file row and a blob write on the way to being told NotFound.
	current, err := db.GetTrigger(ctx, req.GetId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}
	if err != nil {
		log.Z.Error("failed to load trigger for update", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update trigger"))
	}
	// Matches UpdateTriggerByUser's own privacy check: a trigger that is not
	// the caller's own is NotFound, checked here too so no validation, clearance
	// detail or side effect can run against a trigger the caller could not
	// update anyway.
	if current.UserID == nil || *current.UserID != caller.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}

	update := db.TriggerUpdate{
		ID:     req.GetId(),
		UserID: caller.ID,
	}

	if req.HasReply() {
		update.UpdateReply = true
		update.Reply = req.GetReply()
	}
	if req.HasChance() {
		chance := req.GetChance()
		if chance < 0 || chance > trigger.MaxChance {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chance must be between 0 and %d", trigger.MaxChance))
		}
		update.UpdateChance = true
		update.Chance = chance
	}

	if req.HasPhrase() || req.HasMode() {
		effectivePhrase := current.Phrase
		if req.HasPhrase() {
			effectivePhrase = req.GetPhrase()
		}
		// An explicit UNSPECIFIED means ANY, per the enum's own doc comment.
		// Writing the current mode back instead would make the same request
		// behave differently depending on the row it landed on.
		effectiveMode := pb.TriggerMode(current.Mode)
		if req.HasMode() {
			effectiveMode = req.GetMode()
			if effectiveMode == pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED {
				effectiveMode = pb.TriggerMode_TRIGGER_MODE_ANY
			}
		}

		if effectiveMode == pb.TriggerMode_TRIGGER_MODE_REGEX && caller.Clearance < int32(regexClearanceFloor) {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("regex triggers require %s clearance", regexClearanceFloor.String()))
		}
		if _, compileErr := trigger.Compile(effectivePhrase, effectiveMode); compileErr != nil {
			return nil, invalidPatternError(compileErr)
		}

		if req.HasPhrase() {
			update.UpdatePhrase = true
			update.Phrase = req.GetPhrase()
		}
		if req.HasMode() {
			update.UpdateMode = true
			update.Mode = effectiveMode
		}
	}

	// Last, because it is the only step with a side effect outside this
	// transaction: a fetched blob that a later failure abandons is left for the
	// orphan sweep rather than deleted, since another trigger may have deduped
	// onto it.
	if req.HasFileUrl() && req.GetFileUrl() != "" {
		fileID, fetchErr := s.fetchAndStoreFile(ctx, req.GetFileUrl())
		if fetchErr != nil {
			return nil, fetchErr
		}
		update.UpdateFile = true
		update.FileID = fileID
	}

	// Read before the write: once the row is updated (or deleted concurrently)
	// there is nothing left to look up the OLD scope from.
	scopedIDs, err := db.ListTriggerInstanceIDs(ctx, req.GetId())
	if err != nil {
		log.Z.Error("failed to load trigger scope for invalidation", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update trigger"))
	}

	err = db.UpdateTriggerByUser(ctx, update)
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}
	if errors.Is(err, db.ErrExactPhraseTaken) {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("an exact trigger with this phrase already exists"))
	}
	if err != nil {
		log.Z.Error("failed to update trigger", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update trigger"))
	}

	for _, instanceID := range scopedIDs {
		s.cache.Invalidate(instanceID)
	}

	return connect.NewResponse(pb.UpdateTriggerResp_builder{}.Build()), nil
}

// DeleteTrigger soft-deletes the caller's own trigger.
func (s *TriggerServer) DeleteTrigger(ctx context.Context, connReq *connect.Request[pb.DeleteTriggerReq]) (*connect.Response[pb.DeleteTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	// Read before the write, matching UpdateTrigger: after the delete there is
	// nothing left to look up the scope from.
	scopedIDs, err := db.ListTriggerInstanceIDs(ctx, req.GetId())
	if err != nil {
		log.Z.Error("failed to load trigger scope for invalidation", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete trigger"))
	}

	err = db.SoftDeleteTriggerByUser(ctx, req.GetId(), caller.ID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}
	if err != nil {
		log.Z.Error("failed to delete trigger", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete trigger"))
	}

	for _, instanceID := range scopedIDs {
		s.cache.Invalidate(instanceID)
	}

	return connect.NewResponse(pb.DeleteTriggerResp_builder{}.Build()), nil
}

// GetTriggerStats returns a leaderboard derived from action_record, scoped to
// an instance and to a period.
func (s *TriggerServer) GetTriggerStats(ctx context.Context, connReq *connect.Request[pb.GetTriggerStatsReq]) (*connect.Response[pb.GetTriggerStatsResp], error) {
	req := connReq.Msg

	if _, err := callerUser(ctx); err != nil {
		return nil, err
	}

	if !req.HasInstance() || !req.GetInstance().HasPlatformEnum() || !req.GetInstance().HasInstanceMeta() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance is required"))
	}

	// A leaderboard exposes another guild's phrases, chances and fire counts,
	// so the named instance has to be the one the call came from.
	instanceID, err := callerScopedInstance(ctx, req.GetInstance())
	if err != nil {
		return nil, err
	}

	actionType := pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED
	if req.HasActionType() && req.GetActionType() != pb.ActionType_ACTION_TYPE_UNSPECIFIED {
		actionType = req.GetActionType()
	}
	if actionType != pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED && actionType != pb.ActionType_ACTION_TYPE_TRIGGER_CALLED {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("action_type must be TRIGGER_OCCURRED or TRIGGER_CALLED"))
	}

	filter := db.TriggerStatsFilter{
		InstanceID: instanceID,
		ActionType: actionType,
		Limit:      req.GetLimit(),
	}
	if req.HasPeriodStart() {
		start := req.GetPeriodStart().AsTime()
		filter.PeriodStart = &start
	}
	if req.HasPeriodEnd() {
		end := req.GetPeriodEnd().AsTime()
		filter.PeriodEnd = &end
	}

	rows, err := db.ListTriggerStats(ctx, filter)
	if err != nil {
		log.Z.Error("failed to list trigger stats", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load trigger stats"))
	}

	stats := make([]*pb.TriggerStat, 0, len(rows))
	for _, row := range rows {
		mode := pb.TriggerMode(row.Mode)
		stats = append(stats, pb.TriggerStat_builder{
			TriggerId: &row.TriggerID,
			Phrase:    &row.Phrase,
			Count:     &row.Count,
			Chance:    &row.Chance,
			Mode:      &mode,
		}.Build())
	}

	return connect.NewResponse(pb.GetTriggerStatsResp_builder{
		Stats: stats,
	}.Build()), nil
}

// GetFile returns a trigger file's metadata and content. A file is readable
// by a caller who could see a trigger that references it: one they created,
// or one scoped to their own call origin instance. Content is bounded by
// storage.MaxFileBytes as a last line of defence — a row whose blob has grown
// past the cap on disk is refused, not streamed.
func (s *TriggerServer) GetFile(ctx context.Context, connReq *connect.Request[pb.GetFileReq]) (*connect.Response[pb.GetFileResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasFileId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file_id is required"))
	}

	fileRow, err := db.GetFile(ctx, req.GetFileId())
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found"))
	}
	if err != nil {
		log.Z.Error("failed to get file", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}

	// A zero instance id (no origin, e.g. a direct message) simply never
	// matches inside FileVisibleToCaller, leaving the ownership check as the
	// only path to visibility — no special-cased "no origin" branch needed.
	originInstanceID, _ := callerOriginInstanceID(ctx)
	visible, err := db.FileVisibleToCaller(ctx, fileRow.ID, caller.ID, originInstanceID)
	if err != nil {
		log.Z.Error("failed to check file visibility", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}
	if !visible {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found"))
	}

	if s.blobs == nil {
		log.Z.Error("trigger file storage is not configured")
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}

	reader, err := s.blobs.Get(ctx, fileRow.Path)
	if err != nil {
		log.Z.Error("failed to open trigger file blob", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}
	defer func() {
		_ = reader.Close()
	}()

	limited := io.LimitReader(reader, storage.MaxFileBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		log.Z.Error("failed to read trigger file blob", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}
	if int64(len(content)) > storage.MaxFileBytes {
		log.Z.Error("trigger file blob exceeds the size cap", zap.String("file_id", fileRow.ID))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("file exceeds the maximum size"))
	}

	return connect.NewResponse(pb.GetFileResp_builder{
		File:    fileRow.ToProto(displayFilename(fileRow)),
		Content: content,
	}.Build()), nil
}
