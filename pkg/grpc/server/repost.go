package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/repost"
	"github.com/lasikuu/GinBot/pkg/repost/fingerprint"
	"github.com/lasikuu/GinBot/pkg/repost/urlnorm"
	"github.com/lasikuu/GinBot/pkg/storage"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type RepostServer struct {
	ginbotv1connect.UnimplementedRepostServiceHandler

	fetcher *storage.Fetcher
	hasher  *fingerprint.Hasher
	norm    *urlnorm.Canonicaliser
	tiers   repost.Tiers
}

func NewRepostServer() *RepostServer {
	tiers, changed := repost.Tiers{
		Identical: config.Options.Repost.TierIdentical,
		High:      config.Options.Repost.TierHigh,
		Probable:  config.Options.Repost.TierProbable,
	}.Normalise()
	if changed {
		// A floor above repost.MaxDistance breaks the pigeonhole recall guarantee.
		log.Z.Warn("repost confidence tiers were out of range and have been clamped to a valid, monotonic band",
			zap.Int("identical", tiers.Identical), zap.Int("high", tiers.High), zap.Int("probable", tiers.Probable))
	}

	ffmpegPath := config.Options.Repost.FFmpegPath
	if ffmpegPath == "" {
		ffmpegPath = fingerprint.LookupFFmpeg()
	}

	guards := fingerprint.Guards{
		MinWidth:   config.Options.Repost.MinWidth,
		MinHeight:  config.Options.Repost.MinHeight,
		MinEntropy: config.Options.Repost.MinEntropy,
	}

	return newRepostServer(
		storage.NewFetcher(nil, storage.DefaultAllowedHosts(), storage.MaxFileBytes),
		fingerprint.NewHasher(guards, ffmpegPath),
		urlnorm.New(config.Options.Repost.ExcludedHosts),
		tiers,
	)
}

func newRepostServer(fetcher *storage.Fetcher, hasher *fingerprint.Hasher, norm *urlnorm.Canonicaliser, tiers repost.Tiers) *RepostServer {
	return &RepostServer{
		fetcher: fetcher,
		hasher:  hasher,
		norm:    norm,
		tiers:   tiers,
	}
}

// repostIdentity is what a candidate resolved to, whether or not it matched.
type repostIdentity struct {
	kind         int32
	sourceKey    string
	canonicalURL string
	contentHash  []byte
	phash        *int64
}

// CheckRepost looks candidates up and indexes them in the same call.
func (s *RepostServer) CheckRepost(ctx context.Context, connReq *connect.Request[pb.CheckRepostReq]) (*connect.Response[pb.CheckRepostResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	// A direct message has no instance to scope the index to.
	instanceID, ok := callerOriginInstanceID(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this action must be used in a server or room"))
	}

	if !req.HasMessageUid() || req.GetMessageUid() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message_uid is required"))
	}
	if !req.HasAuthorUid() || req.GetAuthorUid() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("author_uid is required"))
	}
	if len(req.GetCandidates()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one candidate is required"))
	}

	origin, _ := interceptor.OriginFromContext(ctx)

	// Best effort: the destination is presentation context, not part of matching.
	var destinationID *int64
	if origin.DestinationUID != "" {
		if id, err := db.GetDestinationIDByMeta(ctx, instanceID, origin.DestinationMeta()); err == nil {
			destinationID = &id
		}
	}

	postedAt := clampPostedAt(req)

	var matches []*pb.RepostMatch

	// Sequential: the candidate cap and the fetcher's own timeout bound the worst case.
	for i, candidate := range req.GetCandidates() {
		// Every remaining Fetch would fail instantly on the cancelled context.
		if ctx.Err() != nil {
			break
		}

		identity, match := s.checkCandidate(ctx, instanceID, caller.ID, candidate)

		// An edit matches but never inserts, or it would match itself.
		if identity != nil && !req.GetEdit() {
			params := db.CreateRepostEntryParams{
				InstanceID:    instanceID,
				DestinationID: destinationID,
				UserID:        caller.ID,
				Kind:          identity.kind,
				SourceKey:     identity.sourceKey,
				CanonicalURL:  identity.canonicalURL,
				ContentHash:   identity.contentHash,
				MsgRef:        model.NewRepostMsgRef(origin.InstanceUID, origin.DestinationUID, req.GetMessageUid(), req.GetAuthorUid()),
				PostedAt:      postedAt,
				PHash:         identity.phash,
			}
			if _, err := db.CreateRepostEntry(ctx, params); err != nil {
				log.Z.Error("failed to index repost entry", zap.Error(err))
			}
		}

		if match != nil {
			candidateIndex := int32(i)
			match.SetCandidateIndex(candidateIndex)
			matches = append(matches, match)
		}
	}

	return connect.NewResponse(pb.CheckRepostResp_builder{Matches: matches}.Build()), nil
}

// postedAtTolerance rejects nonsense; it does not police clock skew.
const postedAtTolerance = 24 * time.Hour

// clampPostedAt falls back to server time when posted_at is not credible: an
// unset Timestamp still reports HasPostedAt and decodes to year 1.
func clampPostedAt(req *pb.CheckRepostReq) time.Time {
	now := time.Now().UTC()
	if !req.HasPostedAt() {
		return now
	}

	claimed := req.GetPostedAt().AsTime().UTC()
	if claimed.Before(now.Add(-postedAtTolerance)) || claimed.After(now.Add(postedAtTolerance)) {
		log.Z.Warn("ignoring an implausible repost posted_at and using server time instead",
			zap.Time("claimed", claimed))
		return now
	}

	return claimed
}

// checkCandidate returns an identity whenever the candidate is worth remembering.
func (s *RepostServer) checkCandidate(ctx context.Context, instanceID int64, excludeUserID string, candidate *pb.RepostCandidate) (*repostIdentity, *pb.RepostMatch) {
	if candidate.GetKind() == pb.RepostKind_REPOST_KIND_LINK {
		return s.checkLink(ctx, instanceID, excludeUserID, candidate.GetUrl())
	}
	return s.checkAttachment(ctx, instanceID, excludeUserID, candidate.GetUrl())
}

func (s *RepostServer) checkLink(ctx context.Context, instanceID int64, excludeUserID string, rawURL string) (*repostIdentity, *pb.RepostMatch) {
	result, err := s.norm.Canonicalise(rawURL)
	if err != nil {
		// Skipped entirely: quoting a message must not flag it as its own repost.
		return nil, nil
	}

	identity := &repostIdentity{
		kind:         int32(pb.RepostKind_REPOST_KIND_LINK.Number()),
		sourceKey:    result.SourceKey,
		canonicalURL: result.CanonicalURL,
	}

	entry, err := db.MatchRepostBySourceKey(ctx, instanceID, result.SourceKey, excludeUserID)
	if errors.Is(err, db.ErrNotFound) {
		return identity, nil
	}
	if err != nil {
		log.Z.Error("failed to match repost by source key", zap.Error(err))
		return identity, nil
	}

	return identity, buildRepostMatch(entry, pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL, 0)
}

// checkAttachment tries exact-bytes matching before perceptual; first hit wins.
func (s *RepostServer) checkAttachment(ctx context.Context, instanceID int64, excludeUserID string, rawURL string) (*repostIdentity, *pb.RepostMatch) {
	fetched, err := s.fetcher.Fetch(ctx, rawURL)
	if err != nil {
		if errors.Is(err, storage.ErrHostNotAllowed) || errors.Is(err, storage.ErrTooLarge) || errors.Is(err, storage.ErrUnsupportedType) {
			// One bad attachment must not lose the rest of the message.
			return nil, nil
		}
		// No zap.Error: a *url.Error embeds the full user-supplied URL.
		log.Z.Error("failed to fetch a repost candidate")
		return nil, nil
	}

	sum, _ := fingerprint.ContentHash(fetched.Content)
	// Derived from the sniffed bytes, never from the client's declared kind.
	resolvedKind := fingerprint.Kind(fetched.MIMEType)

	identity := &repostIdentity{
		kind:        int32(resolvedKind.Number()),
		contentHash: sum,
	}

	entry, err := db.MatchRepostByContentHash(ctx, instanceID, sum, excludeUserID)
	if err == nil {
		return identity, buildRepostMatch(entry, pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL, 0)
	}
	if !errors.Is(err, db.ErrNotFound) {
		log.Z.Error("failed to match repost by content hash", zap.Error(err))
		return identity, nil
	}

	if resolvedKind != pb.RepostKind_REPOST_KIND_IMAGE && resolvedKind != pb.RepostKind_REPOST_KIND_VIDEO {
		// Nothing perceptual to hash; still indexed by content hash above.
		return identity, nil
	}

	phash, err := s.hasher.PerceptualHash(ctx, fetched.Content, fetched.MIMEType)
	if err != nil {
		// Guard outcomes are expected; the content hash still keeps the item indexed.
		return identity, nil
	}

	phashValue := int64(phash)
	identity.phash = &phashValue

	perceptual, err := db.MatchRepostByPerceptualHash(ctx, instanceID, phashValue, repost.Chunks(phash), int32(s.tiers.Probable), excludeUserID)
	if errors.Is(err, db.ErrNotFound) {
		return identity, nil
	}
	if err != nil {
		log.Z.Error("failed to match repost by perceptual hash", zap.Error(err))
		return identity, nil
	}

	confidence := s.tiers.Grade(int(perceptual.Distance))
	if confidence == pb.RepostConfidence_REPOST_CONFIDENCE_UNSPECIFIED {
		// Beyond the configured band; graded independently of the SQL-side ceiling.
		return identity, nil
	}

	return identity, buildRepostMatch(perceptual.Entry, confidence, perceptual.Distance)
}

// buildRepostMatch leaves CandidateIndex for the caller to set.
func buildRepostMatch(entry *model.RepostEntry, confidence pb.RepostConfidence, distance int32) *pb.RepostMatch {
	kind := pb.RepostKind(entry.Kind)
	return pb.RepostMatch_builder{
		Kind:             &kind,
		Confidence:       &confidence,
		Distance:         &distance,
		OriginalPostedAt: timestamppb.New(entry.PostedAt),
		OriginalRef:      entry.MessageRef(),
	}.Build()
}
