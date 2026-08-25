package discord

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
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

// TestRepostAttachmentKindPrefersContentTypeOverTheFilename is all that is
// left of this file's kind coverage, and it is the only part pkg/discord is
// actually responsible for.
//
// The classification tables themselves now live in pkg/repost
// (Kind/KindFromContentType/KindFromFilename) and are tested once there,
// including the drift test that keeps the MIME and extension tables in
// agreement. What is local to this package — and testable nowhere else — is
// the PRECEDENCE rule: Discord populates ContentType for most attachments but
// not all, so the filename is a fallback and never an override.
//
// Every row deliberately makes ContentType and the filename disagree, so a
// classifier consulting the wrong one cannot pass by coincidence.
//
// The kind is a routing hint regardless: the server re-derives the
// authoritative kind from the fetched bytes' sniffed MIME type and discards
// this value, which is exactly why one precedence test is the right amount of
// coverage here rather than a second copy of the classification table.
func TestRepostAttachmentKindPrefersContentTypeOverTheFilename(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		filename    string
		want        pb.RepostKind
	}{
		{
			name:        "content type wins over a disagreeing extension",
			contentType: "image/png",
			filename:    "clip.mp4",
			want:        pb.RepostKind_REPOST_KIND_IMAGE,
		},
		{
			name:        "content type wins the other way round too",
			contentType: "video/mp4",
			filename:    "photo.png",
			want:        pb.RepostKind_REPOST_KIND_VIDEO,
		},
		{
			name:        "an unsupported content type wins over a supported extension",
			contentType: "application/pdf",
			filename:    "photo.png",
			want:        pb.RepostKind_REPOST_KIND_FILE,
		},
		{
			name:        "an absent content type falls back to the filename",
			contentType: "",
			filename:    "clip.mp4",
			want:        pb.RepostKind_REPOST_KIND_VIDEO,
		},
		{
			name:        "an absent content type and an unknown extension is a plain file",
			contentType: "",
			filename:    "data.xyz123",
			want:        pb.RepostKind_REPOST_KIND_FILE,
		},
		{
			name:        "an absent content type and no filename at all is a plain file",
			contentType: "",
			filename:    "",
			want:        pb.RepostKind_REPOST_KIND_FILE,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Driven through repostCandidates rather than repostAttachmentKind
			// directly, so the wiring between the two is covered as well as
			// the precedence rule itself.
			m := &discordgo.Message{
				Attachments: []*discordgo.MessageAttachment{
					attachment("https://cdn.discordapp.com/a", tt.contentType, tt.filename),
				},
			}

			got := repostCandidates(m)
			if len(got) != 1 {
				t.Fatalf("repostCandidates() = %d candidates, want 1", len(got))
			}
			if got[0].GetKind() != tt.want {
				t.Errorf("kind = %v, want %v (ContentType %q, filename %q)",
					got[0].GetKind(), tt.want, tt.contentType, tt.filename)
			}
		})
	}
}

// TestRepostCandidatesPutsLinksBeforeAttachments: the ordering is
// load-bearing, because the cap is applied to the combined list — a message
// with a link and twenty attachments must not lose the link. Asserted on the
// URLs rather than on the kinds, which is strictly stronger: two candidates
// could share a kind and hide a swap.
func TestRepostCandidatesPutsLinksBeforeAttachments(t *testing.T) {
	const linkURL = "https://example.com/a"
	const attachmentURL = "https://cdn.discordapp.com/x.png"

	m := &discordgo.Message{
		Content: "look at " + linkURL,
		Attachments: []*discordgo.MessageAttachment{
			attachment(attachmentURL, "image/png", "x.png"),
		},
	}

	got := repostCandidates(m)
	if len(got) != 2 {
		t.Fatalf("repostCandidates() = %d candidates, want 2", len(got))
	}
	if got[0].GetUrl() != linkURL {
		t.Errorf("candidate 0 url = %q, want the link %q (links come first)", got[0].GetUrl(), linkURL)
	}
	if got[0].GetKind() != pb.RepostKind_REPOST_KIND_LINK {
		t.Errorf("candidate 0 kind = %v, want REPOST_KIND_LINK", got[0].GetKind())
	}
	if got[1].GetUrl() != attachmentURL {
		t.Errorf("candidate 1 url = %q, want the attachment %q", got[1].GetUrl(), attachmentURL)
	}
}

// TestRepostCandidatesEmitsAnAttachmentWithNoRecognisableType: neither a
// usable ContentType nor a recognised extension must still produce a
// candidate. Dropping it would mean an exactly-reposted file of an unusual
// type is never detected at all — FILE-kind entries are still content-hashed
// and matched exactly (docs/plans/wanha.md W6), so there is nothing to gain by
// discarding it client-side.
//
// The kind is not asserted here; pkg/repost owns that. What is asserted is
// that the candidate exists and carries the right URL.
func TestRepostCandidatesEmitsAnAttachmentWithNoRecognisableType(t *testing.T) {
	const url = "https://cdn.discordapp.com/data.xyz123"

	m := &discordgo.Message{
		Attachments: []*discordgo.MessageAttachment{
			attachment(url, "", "data.xyz123"),
		},
	}

	got := repostCandidates(m)
	if len(got) != 1 {
		t.Fatalf("repostCandidates() = %d candidates, want 1; an unrecognised attachment must not be dropped", len(got))
	}
	if got[0].GetUrl() != url {
		t.Errorf("url = %q, want the attachment URL %q", got[0].GetUrl(), url)
	}
}

// TestRepostCandidatesSkipsUnusableAttachments: an attachment with no URL is
// nothing the server could fetch, and CheckRepostReq's own validation rejects
// a candidate with an empty url — so emitting one would fail the WHOLE call,
// taking every other candidate in the message down with it. A nil element is
// covered in the same table because discordgo's slice is of pointers and a
// dereference here would panic on the event goroutine.
func TestRepostCandidatesSkipsUnusableAttachments(t *testing.T) {
	m := &discordgo.Message{
		Attachments: []*discordgo.MessageAttachment{
			nil,
			attachment("", "image/png", "x.png"),
			attachment("https://cdn.discordapp.com/good.png", "image/png", "good.png"),
		},
	}

	got := repostCandidates(m)
	if len(got) != 1 {
		t.Fatalf("repostCandidates() = %d candidates, want 1 (the nil and the URL-less attachment must be skipped)", len(got))
	}
	if got[0].GetUrl() != "https://cdn.discordapp.com/good.png" {
		t.Errorf("url = %q, want the one usable attachment", got[0].GetUrl())
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
// cap on CheckRepostReq.candidates), and exceeding it is not merely wasteful —
// CheckRepostReq.candidates carries max_items = 20, so a 21st candidate makes
// the server reject the entire request and the message goes unchecked.
//
// The cap is enforced in two separate places, once per loop, so both are
// driven: links alone, attachments alone, and the combined case where the
// links have already consumed the whole budget.
func TestRepostCandidatesCapsAtTwenty(t *testing.T) {
	links := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString("https://example.com/")
			b.WriteString(strconv.Itoa(i))
			b.WriteString(" ")
		}
		return b.String()
	}

	attachments := func(n int) []*discordgo.MessageAttachment {
		out := make([]*discordgo.MessageAttachment, 0, n)
		for i := 0; i < n; i++ {
			name := "x" + strconv.Itoa(i) + ".png"
			out = append(out, attachment("https://cdn.discordapp.com/"+name, "image/png", name))
		}
		return out
	}

	tests := []struct {
		name    string
		message *discordgo.Message
	}{
		{
			name:    "links alone",
			message: &discordgo.Message{Content: links(25)},
		},
		{
			name:    "attachments alone",
			message: &discordgo.Message{Attachments: attachments(25)},
		},
		{
			name:    "links and attachments together",
			message: &discordgo.Message{Content: links(15), Attachments: attachments(15)},
		},
		{
			name: "links already fill the budget",
			// The link loop returns at the cap, so the attachment loop must
			// not append even one more on top of a full list.
			message: &discordgo.Message{Content: links(20), Attachments: attachments(5)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repostCandidates(tt.message)
			if len(got) != maxRepostCandidates {
				t.Errorf("repostCandidates() = %d candidates, want capped at %d", len(got), maxRepostCandidates)
			}
		})
	}
}

// TestRepostCandidatesKeepsTheLinkWhenAttachmentsWouldFillTheCap is the
// reason links are gathered first, stated as an assertion rather than left to
// the construction order: a message with one link and twenty images must still
// check the link, which is the cheapest and most commonly reposted candidate
// of the lot.
func TestRepostCandidatesKeepsTheLinkWhenAttachmentsWouldFillTheCap(t *testing.T) {
	const linkURL = "https://example.com/the-link"

	files := make([]*discordgo.MessageAttachment, 0, maxRepostCandidates)
	for i := 0; i < maxRepostCandidates; i++ {
		name := "x" + strconv.Itoa(i) + ".png"
		files = append(files, attachment("https://cdn.discordapp.com/"+name, "image/png", name))
	}

	got := repostCandidates(&discordgo.Message{Content: linkURL, Attachments: files})

	if len(got) != maxRepostCandidates {
		t.Fatalf("repostCandidates() = %d candidates, want capped at %d", len(got), maxRepostCandidates)
	}
	if got[0].GetUrl() != linkURL {
		t.Errorf("candidate 0 url = %q, want the link %q; the attachments crowded it out", got[0].GetUrl(), linkURL)
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
