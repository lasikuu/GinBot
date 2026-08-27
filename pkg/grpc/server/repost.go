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

// RepostServer implements RepostService (WANHA).
type RepostServer struct {
	ginbotv1connect.UnimplementedRepostServiceHandler

	fetcher *storage.Fetcher
	hasher  *fingerprint.Hasher
	norm    *urlnorm.Canonicaliser
	tiers   repost.Tiers
}

// NewRepostServer returns a RepostServer wired from config.
func NewRepostServer() *RepostServer {
	tiers, changed := repost.Tiers{
		Identical: config.Options.Repost.TierIdentical,
		High:      config.Options.Repost.TierHigh,
		Probable:  config.Options.Repost.TierProbable,
	}.Normalise()
	if changed {
		// A misconfigured floor above repost.MaxDistance silently breaks the
		// pigeonhole recall guarantee (see pkg/repost.Chunks), which is
		// exactly the kind of thing that reintroduces false positives. Worth
		// a startup warning rather than a silent clamp.
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

// newRepostServer builds a RepostServer over explicit dependencies. It is a
// test seam: the attachment path is only reachable through the CDN
// allow-list, so without a way to substitute the fetcher and hasher the
// fetch-hash-match path could not be exercised without live external media.
func newRepostServer(fetcher *storage.Fetcher, hasher *fingerprint.Hasher, norm *urlnorm.Canonicaliser, tiers repost.Tiers) *RepostServer {
	return &RepostServer{
		fetcher: fetcher,
		hasher:  hasher,
		norm:    norm,
		tiers:   tiers,
	}
}

// repostIdentity is what a candidate resolved to, once canonicalised or
// fetched, independent of whether it MATCHED anything. It is what gets
// written to repost_entry when the candidate is worth remembering — computed
// regardless of a match, since the community has now seen this instance of
// the content too (or for the first time).
type repostIdentity struct {
	kind         int32
	sourceKey    string
	canonicalURL string
	contentHash  []byte
	phash        *int64
}

// CheckRepost is WANHA's only RPC: it looks candidates up AND remembers them
// in the same call, because splitting that in two would let a client
// remember without looking up.
func (s *RepostServer) CheckRepost(ctx context.Context, connReq *connect.Request[pb.CheckRepostReq]) (*connect.Response[pb.CheckRepostResp], error) {
	req := connReq.Msg

	caller, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}

	// A repost only exists within the community that saw the original (W5):
	// a direct message has no guild or room to scope the index to, so there
	// is nothing to check against and nothing to seed.
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

	// Best effort: a destination that has not been seen before, or a lookup
	// failure, must not fail the whole call. The destination is presentation
	// context for the eventual deep link, not something matching depends on.
	var destinationID *int64
	if origin.DestinationUID != "" {
		if id, err := db.GetDestinationIDByMeta(ctx, instanceID, origin.DestinationMeta()); err == nil {
			destinationID = &id
		}
	}

	postedAt := clampPostedAt(req)

	var matches []*pb.RepostMatch

	// A message with the proto's maximum of 20 candidates serialises up to 20
	// sequential fetches here. That is accepted rather than parallelised:
	// s.fetcher already carries its own 30s timeout per fetch, and the
	// candidate cap plus the client's own concurrency cap on attempts bound
	// how much this can cost in the worst case.
	for i, candidate := range req.GetCandidates() {
		// Once the deadline is gone every remaining Fetch fails instantly on
		// the cancelled context, so carrying on would only produce one log
		// line per remaining candidate for no possible result.
		if ctx.Err() != nil {
			break
		}

		identity, match := s.checkCandidate(ctx, instanceID, caller.ID, candidate)

		// An edit MATCHES but never INSERTS (W8): seeding the index on an
		// edit would make the edited message match itself. Insertion happens
		// regardless of whether this candidate matched anything — the
		// community has now seen this instance of it too, first sighting or
		// not.
		//
		// Note that an edit still pays the full fetch, decode and hash cost
		// above, because matching perceptually is the entire point of checking
		// an edit at all — someone editing a message to add a previously-posted
		// image must still be caught. The saving is only the write.
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
			// Logged, not failed: a lost index row is better than a
			// swallowed WANHA the caller was already told about.
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

	// No matches is an empty response, not an error — the common case.
	return connect.NewResponse(pb.CheckRepostResp_builder{Matches: matches}.Build()), nil
}

// postedAtTolerance is how far from the server's clock a client-supplied
// posted_at may be before it is disbelieved.
//
// Generous in both directions on purpose: it exists to reject nonsense, not to
// police clock skew. A day covers a backfilled message or a client whose clock
// is wrong without rejecting anything legitimate.
const postedAtTolerance = 24 * time.Hour

// clampPostedAt returns the instant to record for a message, falling back to
// server time when the request's own value is not credible.
//
// posted_at is client-supplied and load-bearing twice over: it is the
// tie-break that decides which of two equally-close matches is "the original",
// and it is what the retention sweep measures age against. An unset
// google.protobuf.Timestamp still reports HasPostedAt, and it decodes to year
// 1 — which would make that entry permanently the oldest original in every
// future tie-break, and immediately eligible for deletion by any configured
// retention. A far-future value is the mirror image: never sweepable. Neither
// is worth trusting a client for.
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

// checkCandidate resolves one candidate's identity and, when the community
// has seen it before, its match. identity is non-nil whenever the candidate
// produced something worth remembering, independent of whether it matched.
func (s *RepostServer) checkCandidate(ctx context.Context, instanceID int64, excludeUserID string, candidate *pb.RepostCandidate) (*repostIdentity, *pb.RepostMatch) {
	if candidate.GetKind() == pb.RepostKind_REPOST_KIND_LINK {
		return s.checkLink(ctx, instanceID, excludeUserID, candidate.GetUrl())
	}
	return s.checkAttachment(ctx, instanceID, excludeUserID, candidate.GetUrl())
}

// checkLink canonicalises a link candidate and looks it up by exact
// source_key.
func (s *RepostServer) checkLink(ctx context.Context, instanceID int64, excludeUserID string, rawURL string) (*repostIdentity, *pb.RepostMatch) {
	result, err := s.norm.Canonicalise(rawURL)
	if err != nil {
		// urlnorm.ErrExcluded (the bot's own links, platform message deep
		// links — AC #6) and urlnorm.ErrUnsupported (not an absolute
		// http/https URL) both mean this candidate is silently skipped:
		// neither a match nor an insert. Quoting a message must never flag
		// it as a repost of itself.
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

// checkAttachment fetches an attachment candidate through the CDN allow-list,
// then tries exact-bytes matching before perceptual matching. Cheapest lookup
// first; the first hit wins and the rest are skipped.
func (s *RepostServer) checkAttachment(ctx context.Context, instanceID int64, excludeUserID string, rawURL string) (*repostIdentity, *pb.RepostMatch) {
	fetched, err := s.fetcher.Fetch(ctx, rawURL)
	if err != nil {
		if errors.Is(err, storage.ErrHostNotAllowed) || errors.Is(err, storage.ErrTooLarge) || errors.Is(err, storage.ErrUnsupportedType) {
			// A refusal of the caller's own input. One bad attachment must
			// not lose the links or other attachments in the same message.
			return nil, nil
		}
		// Deliberately NOT zap.Error(err): the error from an HTTP fetch is a
		// *url.Error, whose message embeds the full request URL, so logging it
		// would write user-supplied content into the server log at Error
		// level. The category is all that is actionable here.
		log.Z.Error("failed to fetch a repost candidate")
		return nil, nil
	}

	sum, _ := fingerprint.ContentHash(fetched.Content)
	// The kind stored is derived from the bytes actually fetched and sniffed,
	// never from what the client declared the candidate to be — RepostMatch.kind
	// documents that the original entry's kind may differ from a later
	// candidate's own declared kind for exactly this reason.
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
		// A generic file (or an unrecognised kind) has nothing perceptual to
		// hash. It is still indexed above via ContentHash.
		return identity, nil
	}

	phash, err := s.hasher.PerceptualHash(ctx, fetched.Content, fetched.MIMEType)
	if err != nil {
		// fingerprint.ErrTooSmall, ErrLowEntropy and ErrNoDecoder are all
		// expected, non-fatal guard outcomes (AC #10, #17): no perceptual
		// match is attempted and no fingerprint is stored, but identity above
		// already carries the content hash, so the item stays indexed for
		// exact matching.
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
		// Beyond the configured band even though the SQL-side ceiling let it
		// through (a narrower Probable than the query's own maxDistance
		// argument is not possible here since they are the same value, but
		// grading independently keeps this correct if that ever changes).
		return identity, nil
	}

	return identity, buildRepostMatch(perceptual.Entry, confidence, perceptual.Distance)
}

// buildRepostMatch builds a RepostMatch for an entry that matched.
// CandidateIndex is set by the caller once the candidate's position is known.
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
