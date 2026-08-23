package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ── Assumed symbols from pkg/discord/repost.go (spec §3.7) ────────────────────
//
// Recorded because these are the symbols the tests below depend on, so a change
// to any of them is a deliberate decision rather than a surprise:
//
//	func repostCandidates(m *discordgo.Message) []*pb.RepostCandidate
//	func attemptRepost(s *discordgo.Session, m *discordgo.Message, edit bool)
//	func wanhaContent(match *pb.RepostMatch) string
//	func isHumanMessage(s *discordgo.Session, m *discordgo.Message) bool // interactions.go
//
// attemptRepost is deliberately not tested here: it calls
// client.RepostServiceClient over a real gRPC connection and posts through a
// live discordgo.Session (ChannelMessageSendComplex etc.), and this package's
// own convention (respond_test.go, tools_test.go) draws the testing line at
// pure functions precisely because there is no fake for either of those.
// sessionAs and messageFrom, from interactions_test.go in this same package,
// are reused for isHumanMessage; not redeclared here.

// repostMatch builds a *pb.RepostMatch for wanhaContent tests.
func repostMatch(confidence pb.RepostConfidence, distance int32, postedAt time.Time, ref *pb.MessageRef) *pb.RepostMatch {
	kind := pb.RepostKind_REPOST_KIND_IMAGE
	candidateIndex := int32(0)
	return pb.RepostMatch_builder{
		CandidateIndex:   &candidateIndex,
		Kind:             &kind,
		Confidence:       &confidence,
		Distance:         &distance,
		OriginalPostedAt: timestamppb.New(postedAt),
		OriginalRef:      ref,
	}.Build()
}

func fullMessageRef() *pb.MessageRef {
	instanceUID, destinationUID, messageUID, authorUID := "111", "222", "333", "444"
	return pb.MessageRef_builder{
		InstanceUid:    &instanceUID,
		DestinationUid: &destinationUID,
		MessageUid:     &messageUID,
		AuthorUid:      &authorUID,
	}.Build()
}

// ── wanhaContent ──────────────────────────────────────────────────────────────

// TestWanhaContentIncludesTheTitleTimestampLinkAndPoster is AC15: WANHA,
// relative timestamp, deep link, poster mention.
func TestWanhaContentIncludesTheTitleTimestampLinkAndPoster(t *testing.T) {
	postedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ref := fullMessageRef()
	match := repostMatch(pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL, 0, postedAt, ref)

	got := wanhaContent(match)

	if !strings.Contains(got, "WANHA") {
		t.Errorf("wanhaContent() missing the WANHA title\n---\n%s", got)
	}
	if want := timestampTag(postedAt, timestampRelative); !strings.Contains(got, want) {
		t.Errorf("wanhaContent() missing the relative timestamp tag %q\n---\n%s", want, got)
	}
	wantLink := "https://discord.com/channels/" + ref.GetInstanceUid() + "/" + ref.GetDestinationUid() + "/" + ref.GetMessageUid()
	if !strings.Contains(got, wantLink) {
		t.Errorf("wanhaContent() missing the deep link %q\n---\n%s", wantLink, got)
	}
	wantMention := "<@" + ref.GetAuthorUid() + ">"
	if !strings.Contains(got, wantMention) {
		t.Errorf("wanhaContent() missing the poster mention %q\n---\n%s", wantMention, got)
	}
}

// TestWanhaContentDoesNotRenderAWallClockTimestamp: the whole point of the
// relative tag is that Discord's own client renders it, so the server-side
// string must not also bake in a formatted date the tag would duplicate or
// contradict for viewers in a different timezone.
func TestWanhaContentDoesNotRenderAWallClockTimestamp(t *testing.T) {
	postedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	match := repostMatch(pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL, 0, postedAt, fullMessageRef())

	got := wanhaContent(match)

	for _, absent := range []string{"2026-01-02", "03:04:05"} {
		if strings.Contains(got, absent) {
			t.Errorf("wanhaContent() renders a wall-clock timestamp %q itself instead of leaving it to Discord\n---\n%s", absent, got)
		}
	}
}

// TestWanhaContentWordingVariesByConfidence: PROBABLE must read more
// tentatively than IDENTICAL (docs/plans/wanha.md W9). The exact wording is
// not specified, so this asserts the weaker, content-agnostic property that IS
// specified: the two tiers must not render identically.
func TestWanhaContentWordingVariesByConfidence(t *testing.T) {
	postedAt := time.Now()
	ref := fullMessageRef()

	identical := wanhaContent(repostMatch(pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL, 0, postedAt, ref))
	probable := wanhaContent(repostMatch(pb.RepostConfidence_REPOST_CONFIDENCE_PROBABLE, 6, postedAt, ref))

	if identical == probable {
		t.Error("wanhaContent() renders IDENTICAL and PROBABLE identically; wording must vary by confidence tier")
	}
}

// TestWanhaContentHighAlsoDiffersFromIdentical covers the third tier, so the
// wording differentiation is not merely a two-value special case.
func TestWanhaContentHighAlsoDiffersFromIdentical(t *testing.T) {
	postedAt := time.Now()
	ref := fullMessageRef()

	identical := wanhaContent(repostMatch(pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL, 0, postedAt, ref))
	high := wanhaContent(repostMatch(pb.RepostConfidence_REPOST_CONFIDENCE_HIGH, 2, postedAt, ref))

	if identical == high {
		t.Error("wanhaContent() renders IDENTICAL and HIGH identically; wording must vary by confidence tier")
	}
}

// TestWanhaContentDegradesGracefullyWithAnEmptyRef: "any field may be empty
// for an entry stored by a platform that does not carry it" (repost.proto).
// The renderer must not panic and must not emit a broken mention or a
// malformed deep link full of empty path segments.
func TestWanhaContentDegradesGracefullyWithAnEmptyRef(t *testing.T) {
	emptyRef := pb.MessageRef_builder{}.Build()
	match := repostMatch(pb.RepostConfidence_REPOST_CONFIDENCE_HIGH, 2, time.Now(), emptyRef)

	var got string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("wanhaContent() panicked on an empty MessageRef: %v", r)
			}
		}()
		got = wanhaContent(match)
	}()

	if strings.Contains(got, "<@>") {
		t.Errorf("wanhaContent() rendered an empty mention <@>\n---\n%s", got)
	}
	if strings.Contains(got, "channels///") {
		t.Errorf("wanhaContent() rendered a deep link with empty path segments\n---\n%s", got)
	}
	if !strings.Contains(got, "WANHA") {
		t.Errorf("wanhaContent() with an empty ref still must carry the WANHA title\n---\n%s", got)
	}
}

// TestWanhaContentDegradesGracefullyWithANilRef covers a nil OriginalRef
// outright (rather than a populated-but-empty one), which a hand-built match
// could plausibly carry.
func TestWanhaContentDegradesGracefullyWithANilRef(t *testing.T) {
	match := repostMatch(pb.RepostConfidence_REPOST_CONFIDENCE_HIGH, 2, time.Now(), nil)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("wanhaContent() panicked on a nil OriginalRef: %v", r)
		}
	}()
	_ = wanhaContent(match)
}

// ── repostCandidates ──────────────────────────────────────────────────────────

func attachment(url, contentType, filename string) *discordgo.MessageAttachment {
	return &discordgo.MessageAttachment{URL: url, ContentType: contentType, Filename: filename}
}

// TestRepostCandidatesFromLinksOnly.
func TestRepostCandidatesFromLinksOnly(t *testing.T) {
	m := &discordgo.Message{Content: "check this out https://example.com/a"}

	got := repostCandidates(m)
	if len(got) != 1 {
		t.Fatalf("repostCandidates() = %d candidates, want 1", len(got))
	}
	if got[0].GetKind() != pb.RepostKind_REPOST_KIND_LINK {
		t.Errorf("kind = %v, want REPOST_KIND_LINK", got[0].GetKind())
	}
	if got[0].GetUrl() != "https://example.com/a" {
		t.Errorf("url = %q, want %q", got[0].GetUrl(), "https://example.com/a")
	}
}

// TestRepostCandidatesFromAttachmentsOnly: kind is taken from ContentType when
// present.
func TestRepostCandidatesFromAttachmentsOnly(t *testing.T) {
	m := &discordgo.Message{
		Attachments: []*discordgo.MessageAttachment{
			attachment("https://cdn.discordapp.com/x.png", "image/png", "x.png"),
		},
	}

	got := repostCandidates(m)
	if len(got) != 1 {
		t.Fatalf("repostCandidates() = %d candidates, want 1", len(got))
	}
	if got[0].GetKind() != pb.RepostKind_REPOST_KIND_IMAGE {
		t.Errorf("kind = %v, want REPOST_KIND_IMAGE", got[0].GetKind())
	}
	if got[0].GetUrl() != "https://cdn.discordapp.com/x.png" {
		t.Errorf("url = %q, want the attachment URL", got[0].GetUrl())
	}
}

// TestRepostCandidatesFallsBackToFilenameExtensionForKind: an attachment
// without a usable ContentType still needs a kind, derived from the filename
// extension, so a video attachment does not get silently dropped or
// mis-typed as an image.
func TestRepostCandidatesFallsBackToFilenameExtensionForKind(t *testing.T) {
	m := &discordgo.Message{
		Attachments: []*discordgo.MessageAttachment{
			attachment("https://cdn.discordapp.com/clip.mp4", "", "clip.mp4"),
		},
	}

	got := repostCandidates(m)
	if len(got) != 1 {
		t.Fatalf("repostCandidates() = %d candidates, want 1", len(got))
	}
	if got[0].GetKind() != pb.RepostKind_REPOST_KIND_VIDEO {
		t.Errorf("kind = %v, want REPOST_KIND_VIDEO (from the .mp4 extension)", got[0].GetKind())
	}
}

// TestRepostCandidatesFallsBackToFileKindForAnUnknownExtension: neither a
// usable ContentType nor a recognised extension must still produce SOME
// candidate rather than silently dropping the attachment, graded FILE (exact
// hashing only, per docs/plans/wanha.md W6).
func TestRepostCandidatesFallsBackToFileKindForAnUnknownExtension(t *testing.T) {
	m := &discordgo.Message{
		Attachments: []*discordgo.MessageAttachment{
			attachment("https://cdn.discordapp.com/data.xyz123", "", "data.xyz123"),
		},
	}

	got := repostCandidates(m)
	if len(got) != 1 {
		t.Fatalf("repostCandidates() = %d candidates, want 1", len(got))
	}
	if got[0].GetKind() != pb.RepostKind_REPOST_KIND_FILE {
		t.Errorf("kind = %v, want REPOST_KIND_FILE for an unrecognised extension", got[0].GetKind())
	}
}

// TestRepostCandidatesCombinesLinksAndAttachments: both sources contribute,
// links first per the specified construction order.
func TestRepostCandidatesCombinesLinksAndAttachments(t *testing.T) {
	m := &discordgo.Message{
		Content: "look at https://example.com/a",
		Attachments: []*discordgo.MessageAttachment{
			attachment("https://cdn.discordapp.com/x.png", "image/png", "x.png"),
		},
	}

	got := repostCandidates(m)
	if len(got) != 2 {
		t.Fatalf("repostCandidates() = %d candidates, want 2", len(got))
	}
	if got[0].GetKind() != pb.RepostKind_REPOST_KIND_LINK {
		t.Errorf("candidate 0 kind = %v, want REPOST_KIND_LINK (links first)", got[0].GetKind())
	}
	if got[1].GetKind() != pb.RepostKind_REPOST_KIND_IMAGE {
		t.Errorf("candidate 1 kind = %v, want REPOST_KIND_IMAGE", got[1].GetKind())
	}
}

// TestRepostCandidatesReturnsNilForNeither.
func TestRepostCandidatesReturnsNilForNeither(t *testing.T) {
	m := &discordgo.Message{Content: "just chatting, nothing to see"}

	if got := repostCandidates(m); len(got) != 0 {
		t.Errorf("repostCandidates() = %v, want empty for a message with no links or attachments", got)
	}
}

// TestRepostCandidatesCapsAtTwenty: one message must not be able to cost an
// unbounded number of fetches (repost.proto's own documented reason for the
// cap on CheckRepostReq.candidates).
func TestRepostCandidatesCapsAtTwenty(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 25; i++ {
		b.WriteString("https://example.com/")
		b.WriteString(string(rune('a' + i)))
		b.WriteString(" ")
	}
	m := &discordgo.Message{Content: b.String()}

	got := repostCandidates(m)
	if len(got) != 20 {
		t.Errorf("repostCandidates() = %d candidates for 25 links, want capped at 20", len(got))
	}
}

// TestRepostCandidatesHandlesASpoileredLink: urlnorm.ExtractURLs already
// strips ||spoiler|| wrapping (covered directly in pkg/repost/urlnorm); this
// pins that repostCandidates actually goes through it rather than a simpler,
// spoiler-blind extraction.
func TestRepostCandidatesHandlesASpoileredLink(t *testing.T) {
	m := &discordgo.Message{Content: "||https://example.com/a||"}

	got := repostCandidates(m)
	if len(got) != 1 {
		t.Fatalf("repostCandidates() = %d candidates, want 1", len(got))
	}
	if got[0].GetUrl() != "https://example.com/a" {
		t.Errorf("url = %q, want the spoiler markers stripped", got[0].GetUrl())
	}
}

// ── isHumanMessage ────────────────────────────────────────────────────────────
//
// sessionAs is declared in interactions_test.go in this same package.

func humanMessage(author *discordgo.User) *discordgo.Message {
	return &discordgo.Message{Author: author}
}

// TestIsHumanMessageMirrorsIsHuman: isHumanMessage exists so the edit path
// (which carries a *discordgo.Message, not a *discordgo.MessageCreate) can
// apply the same "ignore bots, ignore ourselves" filter TestIsHuman already
// pins for message creation.
func TestIsHumanMessageMirrorsIsHuman(t *testing.T) {
	tests := []struct {
		name    string
		session *discordgo.Session
		message *discordgo.Message
		want    bool
	}{
		{"a human is dispatched", sessionAs("self"), humanMessage(&discordgo.User{ID: "someone"}), true},
		{"the bot ignores itself", sessionAs("self"), humanMessage(&discordgo.User{ID: "self"}), false},
		{"another bot is ignored", sessionAs("self"), humanMessage(&discordgo.User{ID: "other", Bot: true}), false},
		{"a message with no author is ignored", sessionAs("self"), humanMessage(nil), false},
		{"an unpopulated state does not panic", &discordgo.Session{}, humanMessage(&discordgo.User{ID: "someone"}), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHumanMessage(tt.session, tt.message); got != tt.want {
				t.Errorf("isHumanMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}
