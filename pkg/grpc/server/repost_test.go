package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/repost"
	"github.com/lasikuu/GinBot/pkg/repost/fingerprint"
	"github.com/lasikuu/GinBot/pkg/repost/urlnorm"
	"github.com/lasikuu/GinBot/pkg/storage"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var repostOrigin = callermeta.Origin{InstanceUID: "repost-instance", DestinationUID: "repost-destination"}

func repostCtx(platformUID string) context.Context {
	return originCtx(callerCtx(pb.Platform_PLATFORM_DISCORD, platformUID), repostOrigin)
}

// repostHarness registers one Discord identity at CLEARANCE_REGISTERED.
func repostHarness(t *testing.T, opts ...harnessOption) (*harness, string) {
	t.Helper()

	const platformUID = "repost-caller"
	const userID = "018f0000-0000-7000-8000-0000000000c0"

	dir := newDirectory().add(pb.Platform_PLATFORM_DISCORD, platformUID, testUser(userID, pb.Clearance_CLEARANCE_REGISTERED))
	allOpts := append([]harnessOption{withDirectory(dir)}, opts...)
	h := newHarness(t, allOpts...)
	return h, platformUID
}

// linkCandidate is minimally valid, so a test about another field is not rejected for it.
func linkCandidate(url string) *pb.RepostCandidate {
	kind := pb.RepostKind_REPOST_KIND_LINK
	return pb.RepostCandidate_builder{Kind: &kind, Url: &url}.Build()
}

func imageCandidate(url string) *pb.RepostCandidate {
	kind := pb.RepostKind_REPOST_KIND_IMAGE
	return pb.RepostCandidate_builder{Kind: &kind, Url: &url}.Build()
}

// baseCheckRepostReq is otherwise valid, so a test about one field restates nothing else.
func baseCheckRepostReq(candidates ...*pb.RepostCandidate) *pb.CheckRepostReq {
	messageUID := "message-1"
	authorUID := "author-1"
	return pb.CheckRepostReq_builder{
		Candidates: candidates,
		MessageUid: &messageUID,
		AuthorUid:  &authorUID,
	}.Build()
}

func TestCheckRepostRequiresAResolvableOrigin(t *testing.T) {
	h, uid := repostHarness(t)

	_, err := h.Repost.CheckRepost(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid), // identity, but NO NewOutgoingOrigin
		baseCheckRepostReq(linkCandidate("https://example.com/a")),
	)
	requireCode(t, err, connect.CodeFailedPrecondition)
}

// dir.resolveCount() proves the clearance interceptor actually attempted resolution.
func TestCheckRepostRefusesAnUnregisteredCaller(t *testing.T) {
	dir := newDirectory()
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

func TestCheckRepostRefusesAnAnonymousCaller(t *testing.T) {
	h, _ := repostHarness(t)

	_, err := h.Repost.CheckRepost(anonymousCtx(), baseCheckRepostReq(linkCandidate("https://example.com/a")))
	if err == nil {
		t.Fatal("CheckRepost accepted an anonymous caller (returned OK)")
	}
}

// Covers repeated.min_items = 1; resolveCount 0 proves validation ran before clearance.
func TestCheckRepostValidationRejectsEmptyCandidates(t *testing.T) {
	dir := newDirectory()
	h := newHarness(t, withDirectory(dir))

	_, err := h.Repost.CheckRepost(anonymousCtx(), baseCheckRepostReq() /* no candidates */)
	requireCode(t, err, connect.CodeInvalidArgument)

	if got := dir.resolveCount(); got != 0 {
		t.Errorf("resolveCount() = %d, want 0 (validation must reject before clearance resolves anyone)", got)
	}
}

// Covers (buf.validate.field).repeated.max_items = 20.
func TestCheckRepostValidationRejectsTooManyCandidates(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	candidates := make([]*pb.RepostCandidate, 21)
	for i := range candidates {
		candidates[i] = linkCandidate("https://example.com/a")
	}

	_, err := h.Repost.CheckRepost(anonymousCtx(), baseCheckRepostReq(candidates...))
	requireCode(t, err, connect.CodeInvalidArgument)
}

// Covers message_uid's required + min_len constraint.
func TestCheckRepostValidationRejectsMissingMessageUID(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	authorUID := "author-1"
	req := pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate("https://example.com/a")},
		AuthorUid:  &authorUID,
	}.Build()

	_, err := h.Repost.CheckRepost(anonymousCtx(), req)
	requireCode(t, err, connect.CodeInvalidArgument)
}

// Covers author_uid's required + min_len constraint.
func TestCheckRepostValidationRejectsMissingAuthorUID(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	messageUID := "message-1"
	req := pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate("https://example.com/a")},
		MessageUid: &messageUID,
	}.Build()

	_, err := h.Repost.CheckRepost(anonymousCtx(), req)
	requireCode(t, err, connect.CodeInvalidArgument)
}

// Covers url's required + min_len constraint on RepostCandidate.
func TestCheckRepostValidationRejectsACandidateWithNoURL(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	kind := pb.RepostKind_REPOST_KIND_LINK
	req := baseCheckRepostReq(pb.RepostCandidate_builder{Kind: &kind}.Build())

	_, err := h.Repost.CheckRepost(anonymousCtx(), req)
	requireCode(t, err, connect.CodeInvalidArgument)
}

// Covers url's max_len = 2048.
func TestCheckRepostValidationRejectsAnOverlongURL(t *testing.T) {
	h := newHarness(t, withDirectory(newDirectory()))

	overlong := "https://example.com/" + strings.Repeat("a", 2048)
	req := baseCheckRepostReq(linkCandidate(overlong))

	_, err := h.Repost.CheckRepost(anonymousCtx(), req)
	requireCode(t, err, connect.CodeInvalidArgument)
}

// CheckRepost resolves its instance from origin metadata before looking at any
// candidate, so even the skipped cases need a database: repost_integration_test.go.

func newRepostServerForTest(fetcher *storage.Fetcher, hasher *fingerprint.Hasher, norm *urlnorm.Canonicaliser, tiers repost.Tiers) *RepostServer {
	return newRepostServer(fetcher, hasher, norm, tiers)
}

// posted_at is client-supplied: a zero Timestamp still reports HasPostedAt and decodes
// to year 1, and Discord sends one on partial MESSAGE_UPDATE payloads.
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
