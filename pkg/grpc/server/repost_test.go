package server

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/repost"
	"github.com/lasikuu/GinBot/pkg/repost/fingerprint"
	"github.com/lasikuu/GinBot/pkg/repost/urlnorm"
	"github.com/lasikuu/GinBot/pkg/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// repostOrigin is a fixed, valid call origin: an instance and a destination,
// so every test below is exercising the SAME "this call has a real guild and
// channel" shape unless it deliberately omits the origin.
var repostOrigin = callermeta.Origin{InstanceUID: "repost-instance", DestinationUID: "repost-destination"}

// repostCtx attaches both caller identity and call origin, in that order —
// originCtx must run after callerCtx, matching triggerCtx in
// trigger_integration_test.go.
func repostCtx(platformUID string) context.Context {
	return originCtx(callerCtx(pb.Platform_PLATFORM_DISCORD, platformUID), repostOrigin)
}

// ── Assumed symbols from pkg/grpc/server/repost.go (spec §3.7) ───────────────
//
// Recorded because these are the symbols the tests depend on; the tests below
// were originally written against the
// specified interface reproduced in the test-authoring brief:
//
//	type RepostServer struct { pb.UnimplementedRepostServiceServer; /* unexported */ }
//	func NewRepostServer() *RepostServer
//	func newRepostServer(fetcher *storage.Fetcher, hasher *fingerprint.Hasher, norm *urlnorm.Canonicaliser, tiers repost.Tiers) *RepostServer
//	func (s *RepostServer) CheckRepost(ctx context.Context, req *pb.CheckRepostReq) (*pb.CheckRepostResp, error)
//
// Any path that reaches pkg/db needs a live Postgres, so these tests are
// deliberately confined to paths that are refused before the handler ever
// resolves an instance: origin/clearance rejection and protovalidate
// rejection. Everything that reaches the handler body — including candidates
// that are merely skipped, like an excluded link or a refused attachment
// fetch — needs a database, because CheckRepost resolves the calling
// instance from origin metadata before it looks at any candidate at all. Those
// live in repost_integration_test.go.

// repostHarness registers one Discord identity at CLEARANCE_REGISTERED, the
// floor every other guarded RPC in this package requires, mirroring
// triggerHarness in trigger_test.go.
func repostHarness(t *testing.T, opts ...harnessOption) (*harness, string) {
	t.Helper()

	const platformUID = "repost-caller"
	const userID = "018f0000-0000-7000-8000-0000000000c0"

	dir := newDirectory().add(pb.Platform_PLATFORM_DISCORD, platformUID, testUser(userID, pb.Clearance_CLEARANCE_REGISTERED))
	allOpts := append([]harnessOption{withDirectory(dir)}, opts...)
	h := newHarness(t, allOpts...)
	return h, platformUID
}

// linkCandidate builds a minimally valid link candidate, so tests about a
// DIFFERENT field's validation are not accidentally rejected for missing an
// unrelated required field.
func linkCandidate(url string) *pb.RepostCandidate {
	kind := pb.RepostKind_REPOST_KIND_LINK
	return pb.RepostCandidate_builder{Kind: &kind, Url: &url}.Build()
}

func imageCandidate(url string) *pb.RepostCandidate {
	kind := pb.RepostKind_REPOST_KIND_IMAGE
	return pb.RepostCandidate_builder{Kind: &kind, Url: &url}.Build()
}

// baseCheckRepostReq builds an otherwise-valid CheckRepostReq around the given
// candidates, so a test about one field does not have to restate every other
// required field.
func baseCheckRepostReq(candidates ...*pb.RepostCandidate) *pb.CheckRepostReq {
	messageUID := "message-1"
	authorUID := "author-1"
	return pb.CheckRepostReq_builder{
		Candidates: candidates,
		MessageUid: &messageUID,
		AuthorUid:  &authorUID,
	}.Build()
}

// ── No resolvable origin ──────────────────────────────────────────────────────

// TestCheckRepostRequiresAResolvableOrigin: a call with caller identity but no
// origin metadata (a direct message, in Discord terms) must be refused with
// FailedPrecondition, per the spec's explicit statement of this behaviour.
func TestCheckRepostRequiresAResolvableOrigin(t *testing.T) {
	h, uid := repostHarness(t)

	_, err := h.Repost.CheckRepost(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid), // identity, but NO NewOutgoingOrigin
		baseCheckRepostReq(linkCandidate("https://example.com/a")),
	)
	requireCode(t, err, codes.FailedPrecondition)
}

// ── Clearance / caller resolution ────────────────────────────────────────────

// TestCheckRepostRefusesAnUnregisteredCaller: a caller whose platform identity
// the directory does not know must be refused by the clearance interceptor,
// before the handler ever runs. dir.resolveCount() proves the interceptor
// actually attempted resolution, distinguishing this from any other reason
// the call might fail.
func TestCheckRepostRefusesAnUnregisteredCaller(t *testing.T) {
	dir := newDirectory() // nobody registered
	h := newHarness(t, withDirectory(dir))

	ctx := repostCtx("someone-not-registered")

	_, err := h.Repost.CheckRepost(ctx, baseCheckRepostReq(linkCandidate("https://example.com/a")))
	if err == nil {
		t.Fatal("CheckRepost accepted an unregistered caller (returned OK)")
	}
	if got := dir.resolveCount(); got != 1 {
		t.Errorf("resolveCount() = %d, want 1 (the clearance interceptor must have attempted resolution)", got)
	}
}

// TestCheckRepostRefusesAnAnonymousCaller: a call carrying no caller identity
// metadata at all must never be treated as OK, matching the convention in
// TestAllTriggerRPCsRefuseAnAnonymousCaller.
func TestCheckRepostRefusesAnAnonymousCaller(t *testing.T) {
	h, _ := repostHarness(t)

	_, err := h.Repost.CheckRepost(anonymousCtx(), baseCheckRepostReq(linkCandidate("https://example.com/a")))
	if err == nil {
		t.Fatal("CheckRepost accepted an anonymous caller (returned OK)")
	}
}

// ── Validation, before the handler runs ──────────────────────────────────────

// TestCheckRepostValidationRejectsEmptyCandidates covers
// (buf.validate.field).repeated.min_items = 1. An anonymous context is used
// deliberately: validation runs before clearance in the interceptor chain, so
// a malformed request must be rejected regardless of who is calling — and
// dir.resolveCount() staying at 0 is the proof that clearance was never
// reached.
func TestCheckRepostValidationRejectsEmptyCandidates(t *testing.T) {
	dir := newDirectory()
	h := newHarness(t, withDirectory(dir))

	_, err := h.Repost.CheckRepost(anonymousCtx(), baseCheckRepostReq() /* no candidates */)
	requireCode(t, err, codes.InvalidArgument)

	if got := dir.resolveCount(); got != 0 {
		t.Errorf("resolveCount() = %d, want 0 (validation must reject before clearance resolves anyone)", got)
	}
}

// TestCheckRepostValidationRejectsTooManyCandidates covers
// (buf.validate.field).repeated.max_items = 20.
func TestCheckRepostValidationRejectsTooManyCandidates(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	candidates := make([]*pb.RepostCandidate, 21)
	for i := range candidates {
		candidates[i] = linkCandidate("https://example.com/a")
	}

	_, err := h.Repost.CheckRepost(anonymousCtx(), baseCheckRepostReq(candidates...))
	requireCode(t, err, codes.InvalidArgument)
}

// TestCheckRepostValidationRejectsMissingMessageUID covers message_uid's
// required + min_len constraint.
func TestCheckRepostValidationRejectsMissingMessageUID(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	authorUID := "author-1"
	req := pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate("https://example.com/a")},
		AuthorUid:  &authorUID,
	}.Build()

	_, err := h.Repost.CheckRepost(anonymousCtx(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestCheckRepostValidationRejectsMissingAuthorUID covers author_uid's
// required + min_len constraint.
func TestCheckRepostValidationRejectsMissingAuthorUID(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	messageUID := "message-1"
	req := pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate("https://example.com/a")},
		MessageUid: &messageUID,
	}.Build()

	_, err := h.Repost.CheckRepost(anonymousCtx(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestCheckRepostValidationRejectsACandidateWithNoURL covers url's required +
// min_len constraint on RepostCandidate.
func TestCheckRepostValidationRejectsACandidateWithNoURL(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	kind := pb.RepostKind_REPOST_KIND_LINK
	req := baseCheckRepostReq(pb.RepostCandidate_builder{Kind: &kind}.Build())

	_, err := h.Repost.CheckRepost(anonymousCtx(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestCheckRepostValidationRejectsAnOverlongURL covers url's max_len = 2048.
func TestCheckRepostValidationRejectsAnOverlongURL(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	overlong := "https://example.com/" + strings.Repeat("a", 2048)
	req := baseCheckRepostReq(linkCandidate(overlong))

	_, err := h.Repost.CheckRepost(anonymousCtx(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// Excluded links, unparseable URLs and refused attachment fetches are all
// "skipped silently" cases per the spec — but CheckRepost resolves the
// calling instance from origin metadata BEFORE it looks at any candidate
// (mirroring trigger.go's callerOriginInstanceID, which calls
// db.GetInstanceByMeta), so exercising any of them through the RPC needs a
// real database even though the candidate itself never reaches one. Those
// tests live in repost_integration_test.go
// (TestCheckRepostSkipsAnExcludedLinkWithoutError,
// TestCheckRepostSkipsAnUnsupportedURLWithoutError,
// TestCheckRepostSkipsARefusedAttachmentFetchWithoutError), not here.

// newRepostServerForTest is a thin wrapper around the unexported constructor
// so every repost test in this package has one clearly-named call site if the
// constructor's argument order or name needs reconciling against the real
// implementation.
func newRepostServerForTest(fetcher *storage.Fetcher, hasher *fingerprint.Hasher, norm *urlnorm.Canonicaliser, tiers repost.Tiers) *RepostServer {
	return newRepostServer(fetcher, hasher, norm, tiers)
}

// ── posted_at is client-supplied and therefore not trusted ──────────────────

// TestClampPostedAtRejectsImplausibleValues covers a value that is
// load-bearing twice: posted_at is the tie-break deciding which of two equally
// close matches counts as "the original", and it is what the retention sweep
// measures age against.
//
// The unset-Timestamp case is the one that actually bites. A zero
// google.protobuf.Timestamp still reports HasPostedAt, and it decodes to year
// 1 — which would make that entry permanently the oldest "original" in every
// future tie-break AND immediately eligible for deletion under any configured
// retention. Discord sends an unset timestamp on some partial MESSAGE_UPDATE
// payloads, so this is a real path, not a hypothetical one.
func TestClampPostedAtRejectsImplausibleValues(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name      string
		postedAt  *timestamppb.Timestamp
		wantServe bool // true when the server's own clock must be substituted
	}{
		{name: "unset falls back to server time", postedAt: nil, wantServe: true},
		{
			name:      "a zero timestamp is year 1 and must not be believed",
			postedAt:  &timestamppb.Timestamp{},
			wantServe: true,
		},
		{
			name:      "the distant past is not believed",
			postedAt:  timestamppb.New(now.AddDate(-5, 0, 0)),
			wantServe: true,
		},
		{
			name:      "the distant future is not believed, or the entry is never sweepable",
			postedAt:  timestamppb.New(now.AddDate(500, 0, 0)),
			wantServe: true,
		},
		{
			name:      "a plausible recent instant is kept",
			postedAt:  timestamppb.New(now.Add(-2 * time.Hour)),
			wantServe: false,
		},
		{
			name:      "a small clock skew into the future is tolerated",
			postedAt:  timestamppb.New(now.Add(30 * time.Second)),
			wantServe: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := pb.CheckRepostReq_builder{}
			if tt.postedAt != nil {
				builder.PostedAt = tt.postedAt
			}

			before := time.Now().UTC()
			got := clampPostedAt(builder.Build())
			after := time.Now().UTC()

			if tt.wantServe {
				if got.Before(before) || got.After(after) {
					t.Errorf("clampPostedAt = %v, want the server's own clock between %v and %v", got, before, after)
				}
				return
			}

			want := tt.postedAt.AsTime().UTC()
			if !got.Equal(want) {
				t.Errorf("clampPostedAt = %v, want the request's own value %v", got, want)
			}
		})
	}
}
