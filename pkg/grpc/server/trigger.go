package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strconv"

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

// regexClearanceFloor gates TRIGGER_MODE_REGEX in-handler, above the per-RPC
// floor: a regex runs against every message on the instances it is scoped to.
const regexClearanceFloor = pb.Clearance_CLEARANCE_MODERATOR

const triggerFileKeyPrefix = "trigger/"

// mimeExtensions maps a sniffed MIME type to an extension; no original filename is stored.
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

type TriggerServer struct {
	ginbotv1connect.UnimplementedTriggerServiceHandler

	cache   *trigger.Cache
	limiter *trigger.ForcedLimiter
	fetcher *storage.Fetcher
	blobs   storage.Storage
	roll    trigger.Roller
}

func NewTriggerServer() *TriggerServer {
	return newTriggerServer(
		storage.NewFetcher(nil, storage.DefaultAllowedHosts(), storage.MaxFileBytes),
		storage.Default(),
	)
}

func newTriggerServer(fetcher *storage.Fetcher, blobs storage.Storage) *TriggerServer {
	s := &TriggerServer{
		limiter: trigger.NewForcedLimiter(nil),
		fetcher: fetcher,
		blobs:   blobs,
		// A chance roll, not a security decision; math/rand/v2's source is concurrency-safe.
		roll: rand.IntN,
	}
	s.cache = trigger.NewCache(s.loadCandidates)

	return s
}

// PruneForcedLimiter must be called periodically: Allow itself never prunes.
func (s *TriggerServer) PruneForcedLimiter() {
	s.limiter.Prune()
}

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
			// No zap.Error: a regexp compile error embeds the pattern text, which must not be logged.
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

// resolveInstance never creates: the origin interceptor already bootstrapped it.
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

// resolveTriggerID accepts either a canonical uuid or a positive decimal ref
// (see ADR-0039). A ref that resolves to nothing returns the same NotFound the
// caller's own visibility check would, so resolution itself cannot enumerate
// refs: it must fail exactly like an ordinary missing id.
func resolveTriggerID(ctx context.Context, raw string) (string, error) {
	ref, convErr := strconv.ParseInt(raw, 10, 64)
	if convErr != nil || ref <= 0 {
		return raw, nil
	}

	row, err := db.GetTriggerByRef(ctx, ref)
	if errors.Is(err, db.ErrNotFound) {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}
	if err != nil {
		log.Z.Error("failed to resolve trigger ref", zap.Error(err))
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve trigger"))
	}

	return row.ID, nil
}

// callerOriginInstanceID reports ok false with no origin, such as a direct message.
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

// callerScopedInstance refuses any instance but the origin, since instance_meta is public.
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

func displayFilename(file *model.File) string {
	return file.ID + mimeExtensions[file.MimeType]
}

// invalidPatternError avoids echoing the phrase, which a regexp error would embed.
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

// fetchAndStoreFile writes the blob only when the row is new: content-hash dedupe.
func (s *TriggerServer) fetchAndStoreFile(ctx context.Context, fileURL string) (string, error) {
	fetched, err := s.fetcher.Fetch(ctx, fileURL)
	if err != nil {
		return "", mapFetchError(err)
	}

	// Fan-out by the first two hex characters of the hash, per pkg/storage's key layout.
	key := triggerFileKeyPrefix + fetched.Hash[:2] + "/" + fetched.Hash

	fileID, inserted, err := db.GetOrCreateFileByHash(ctx, fetched.Hash, key, fetched.MIMEType, int32(len(fetched.Content)))
	if err != nil {
		log.Z.Error("failed to resolve trigger file row", zap.Error(err))
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("failed to store file"))
	}

	if !inserted {
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

// resolveScopeInstances refuses any instance but the origin, rather than filtering.
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

func (s *TriggerServer) buildTriggerFile(ctx context.Context, fileID string) (*pb.TriggerFile, error) {
	fileRow, err := db.GetFile(ctx, fileID)
	if errors.Is(err, db.ErrNotFound) {
		// file_id is a foreign key, so a missing row means an out-of-band hard delete.
		log.Z.Error("trigger references a missing file row", zap.String("file_id", fileID))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load trigger file"))
	}
	if err != nil {
		log.Z.Error("failed to load trigger file", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load trigger file"))
	}

	return fileRow.ToProto(displayFilename(fileRow)), nil
}

func (s *TriggerServer) buildTryTriggerResp(ctx context.Context, triggerID string) (*pb.TryTriggerResp, error) {
	row, err := db.GetTrigger(ctx, triggerID)
	if errors.Is(err, db.ErrNotFound) {
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
		// Impossible per chk_reply_or_file.
		log.Z.Error("trigger row has neither a reply nor a file", zap.String("trigger_id", id))
		return pb.TryTriggerResp_builder{}.Build(), nil
	}

	return builder.Build(), nil
}

// TryTrigger matches against the cached set; nothing is queried unless it matches.
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
		return connect.NewResponse(pb.TryTriggerResp_builder{}.Build()), nil
	}

	actionType := pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED
	if req.GetForced() {
		if !s.limiter.Allow(caller.ID) {
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

	if err := db.RecordTriggerFire(ctx, actionType, selected.ID, caller.ID); err != nil {
		log.Z.Error("failed to record trigger fire", zap.Error(err))
	}

	return connect.NewResponse(resp), nil
}

// ExecTrigger always fires: no chance roll and no rate limit.
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

	id, err := resolveTriggerID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	row, err := db.GetTrigger(ctx, id)
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
		// NotFound, so a caller cannot learn the id exists in another guild.
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

// GetTrigger is visible to the creator and to callers on an instance it is scoped to.
func (s *TriggerServer) GetTrigger(ctx context.Context, connReq *connect.Request[pb.GetTriggerReq]) (*connect.Response[pb.GetTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	id, err := resolveTriggerID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	row, err := db.GetTrigger(ctx, id)
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

// ListTriggers must set one of the two predicates: both unset makes
// db.ListTriggers return every trigger in the database.
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

	triggerIDs := make([]string, 0, len(rows))
	fileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		triggerIDs = append(triggerIDs, row.ID)
		if row.FileID != nil && *row.FileID != "" {
			fileIDs = append(fileIDs, *row.FileID)
		}
	}

	instancesByTrigger, err := db.GetTriggerInstancesBatch(ctx, triggerIDs)
	if err != nil {
		log.Z.Error("failed to load trigger instances", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list triggers"))
	}

	filesByID, err := db.GetFilesByIDs(ctx, fileIDs)
	if err != nil {
		log.Z.Error("failed to load trigger files", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list triggers"))
	}

	out := make([]*pb.Trigger, 0, len(rows))
	for _, row := range rows {
		var file *pb.TriggerFile
		if row.FileID != nil && *row.FileID != "" {
			fileRow, ok := filesByID[*row.FileID]
			if !ok {
				// file_id is a foreign key, so a missing row means an out-of-band
				// hard delete; buildTriggerFile treats the single-row case the same.
				log.Z.Error("trigger references a missing file row", zap.String("file_id", *row.FileID))
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list triggers"))
			}
			file = fileRow.ToProto(displayFilename(fileRow))
		}

		out = append(out, row.ToProto(file, instancesByTrigger[row.ID]))
	}

	return connect.NewResponse(pb.ListTriggersResp_builder{
		Triggers: out,
	}.Build()), nil
}

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

	triggerID, ref, err := db.CreateTrigger(ctx, db.CreateTriggerParams{
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
		// An abandoned blob is left for the GC job; another trigger may have deduped onto it.
		log.Z.Error("failed to create trigger", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create trigger"))
	}

	for _, instanceID := range instanceIDs {
		s.cache.Invalidate(instanceID)
	}

	return connect.NewResponse(pb.CreateTriggerResp_builder{
		Id:  &triggerID,
		Ref: &ref,
	}.Build()), nil
}

// UpdateTrigger validates the combination that will be stored, not the changed field alone.
func (s *TriggerServer) UpdateTrigger(ctx context.Context, connReq *connect.Request[pb.UpdateTriggerReq]) (*connect.Response[pb.UpdateTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	id, err := resolveTriggerID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	// Ownership first, or an unauthorised caller still causes a CDN download and a blob write.
	current, err := db.GetTrigger(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}
	if err != nil {
		log.Z.Error("failed to load trigger for update", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update trigger"))
	}
	// Duplicates UpdateTriggerByUser's check so nothing runs against another's trigger.
	if current.UserID == nil || *current.UserID != caller.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger not found"))
	}

	update := db.TriggerUpdate{
		ID:     id,
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
		// An explicit UNSPECIFIED means ANY, not "keep the current mode".
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

	// Last: the only step with a side effect outside the transaction.
	if req.HasFileUrl() && req.GetFileUrl() != "" {
		fileID, fetchErr := s.fetchAndStoreFile(ctx, req.GetFileUrl())
		if fetchErr != nil {
			return nil, fetchErr
		}
		update.UpdateFile = true
		update.FileID = fileID
	}

	// Read before the write; afterwards the old scope is unrecoverable.
	scopedIDs, err := db.ListTriggerInstanceIDs(ctx, id)
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

func (s *TriggerServer) DeleteTrigger(ctx context.Context, connReq *connect.Request[pb.DeleteTriggerReq]) (*connect.Response[pb.DeleteTriggerResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	if !req.HasId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	id, err := resolveTriggerID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	// Read before the write; afterwards the scope is unrecoverable.
	scopedIDs, err := db.ListTriggerInstanceIDs(ctx, id)
	if err != nil {
		log.Z.Error("failed to load trigger scope for invalidation", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete trigger"))
	}

	err = db.SoftDeleteTriggerByUser(ctx, id, caller.ID)
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

func (s *TriggerServer) GetTriggerStats(ctx context.Context, connReq *connect.Request[pb.GetTriggerStatsReq]) (*connect.Response[pb.GetTriggerStatsResp], error) {
	req := connReq.Msg

	if _, err := callerUser(ctx); err != nil {
		return nil, err
	}

	if !req.HasInstance() || !req.GetInstance().HasPlatformEnum() || !req.GetInstance().HasInstanceMeta() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance is required"))
	}

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

// GetFileChunkBytes must stay below the message caps both ends install, or
// GetFile fails at the transport for every file.
const GetFileChunkBytes = 1 << 20

// GetFile streams one meta chunk then content chunks. Every authorisation check
// runs before the first Send, since a sent byte cannot be recalled.
func (s *TriggerServer) GetFile(ctx context.Context, connReq *connect.Request[pb.GetFileReq], stream *connect.ServerStream[pb.GetFileChunk]) error {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return err
	}

	if !req.HasFileId() {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file_id is required"))
	}

	fileRow, err := db.GetFile(ctx, req.GetFileId())
	if errors.Is(err, db.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found"))
	}
	if err != nil {
		log.Z.Error("failed to get file", zap.Error(err))
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}

	// A zero instance id never matches in FileVisibleToCaller, so no origin needs no branch.
	originInstanceID, _ := callerOriginInstanceID(ctx)
	visible, err := db.FileVisibleToCaller(ctx, fileRow.ID, caller.ID, originInstanceID)
	if err != nil {
		log.Z.Error("failed to check file visibility", zap.Error(err))
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}
	if !visible {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found"))
	}

	// Cheap pre-check; the row and the blob can disagree, so the running total is the authority.
	if int64(fileRow.ByteSize) > storage.MaxFileBytes {
		log.Z.Error("trigger file row exceeds the size cap", zap.String("file_id", fileRow.ID))
		return connect.NewError(connect.CodeInternal, fmt.Errorf("file exceeds the maximum size"))
	}

	if s.blobs == nil {
		log.Z.Error("trigger file storage is not configured")
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}

	reader, err := s.blobs.Get(ctx, fileRow.Path)
	if err != nil {
		log.Z.Error("failed to open trigger file blob", zap.Error(err))
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
	}
	defer func() {
		_ = reader.Close()
	}()

	if err := stream.Send(pb.GetFileChunk_builder{
		Meta: pb.GetFileMeta_builder{
			File: fileRow.ToProto(displayFilename(fileRow)),
		}.Build(),
	}.Build()); err != nil {
		return err
	}

	// Reused: connect's envelope writer copies buf[:n] synchronously within Send
	// and keeps no reference to it.
	buf := make([]byte, GetFileChunkBytes)
	// Incremented before the cap test, so an over-cap chunk is refused rather than sent.
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, readErr := reader.Read(buf)
		if n > 0 {
			read += int64(n)
			// Can fire with bytes already sent; an errored stream means an incomplete file.
			if read > storage.MaxFileBytes {
				log.Z.Error("trigger file blob exceeds the size cap", zap.String("file_id", fileRow.ID))
				return connect.NewError(connect.CodeInternal, fmt.Errorf("file exceeds the maximum size"))
			}

			if sendErr := stream.Send(pb.GetFileChunk_builder{
				Content: buf[:n],
			}.Build()); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			log.Z.Error("failed to read trigger file blob", zap.Error(readErr))
			return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get file"))
		}
	}
}
