package discord

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/repost"
	"github.com/lasikuu/GinBot/pkg/repost/urlnorm"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxRepostCandidates mirrors CheckRepostReq.candidates' buf.validate
// max_items=20: one message must not be able to cost an unbounded number of
// server-side fetches.
const maxRepostCandidates = 20

// repostAttemptTimeout bounds one whole CheckRepost call, matching
// triggerAttemptTimeout's reasoning: the server's own fetcher already carries
// a per-fetch timeout, this is the outer budget for the RPC as a whole.
const repostAttemptTimeout = 15 * time.Second

// maxConcurrentRepostAttempts caps how many CheckRepost calls are in flight
// at once, for the same reason maxConcurrentTriggerAttempts does: an
// attachment-heavy message costs real memory and network fan-out server-side,
// and discordgo dispatches every event on its own goroutine with no
// backpressure of its own.
const maxConcurrentRepostAttempts = 4

// repostAttemptSlots is that cap. Acquisition is non-blocking: dropping a
// repost check under load is the right failure — a bot that comments on
// ordinary conversation, late, is worse than one that occasionally misses.
var repostAttemptSlots = make(chan struct{}, maxConcurrentRepostAttempts)

// repostCandidates extracts the links and attachments worth checking from a
// message, capped at maxRepostCandidates. Links come first: a message with
// both a link and enough attachments to hit the cap should not lose the link
// to attachment ordering.
func repostCandidates(m *discordgo.Message) []*pb.RepostCandidate {
	var candidates []*pb.RepostCandidate

	for _, rawURL := range urlnorm.ExtractURLs(m.Content) {
		if len(candidates) >= maxRepostCandidates {
			return candidates
		}
		linkURL := rawURL
		kind := pb.RepostKind_REPOST_KIND_LINK
		candidates = append(candidates, pb.RepostCandidate_builder{
			Kind: &kind,
			Url:  &linkURL,
		}.Build())
	}

	for _, attachment := range m.Attachments {
		if len(candidates) >= maxRepostCandidates {
			break
		}
		if attachment == nil || attachment.URL == "" {
			continue
		}

		attachmentURL := attachment.URL
		kind := repostAttachmentKind(attachment)
		candidates = append(candidates, pb.RepostCandidate_builder{
			Kind: &kind,
			Url:  &attachmentURL,
		}.Build())
	}

	return candidates
}

// repostAttachmentKind is a routing hint only: the server re-derives the
// authoritative kind from the fetched bytes' sniffed MIME type regardless
// (fingerprint.Kind), since a client-declared kind is not trusted for storage,
// and the only distinction it acts on is LINK versus not-LINK. The declared
// value is still kept truthful rather than filled in with a placeholder — and
// it now comes from repost's table, the same one the server derives from, so
// the two cannot disagree the way they used to.
//
// ContentType is preferred when Discord supplies it; the filename extension is
// the fallback for the attachments that arrive without one.
func repostAttachmentKind(a *discordgo.MessageAttachment) pb.RepostKind {
	if a.ContentType != "" {
		return repost.KindFromContentType(a.ContentType)
	}
	return repost.KindFromFilename(a.Filename)
}

// attemptRepost offers one message to the server and posts WANHA for
// anything the community has already seen.
//
// It shares commandContext with the command and trigger paths so identity and
// origin travel as metadata, and it renders through respondChat so
// truncation, mention suppression and reply threading are the same code a
// command reply goes through. A non-match, a dropped attempt under load, and
// a failed RPC all say NOTHING: the alternative is a bot that comments on
// ordinary conversation.
func attemptRepost(s *discordgo.Session, m *discordgo.Message, edit bool, clients *client.Clients) {
	if m == nil || m.GuildID == "" {
		// A direct message belongs to no guild, and a repost only exists
		// within the community that saw the original (W5). The server refuses
		// these with FailedPrecondition, so calling it anyway would turn every
		// DM containing a link into an error log line.
		return
	}

	candidates := repostCandidates(m)
	if len(candidates) == 0 {
		// No links, no attachments: nothing to check, so this costs no round
		// trip at all.
		return
	}

	select {
	case repostAttemptSlots <- struct{}{}:
		defer func() { <-repostAttemptSlots }()
	default:
		log.Z.Debug("dropped a repost check; too many already in flight.")
		return
	}

	ctx, cancel := context.WithTimeout(commandContext(clients, m.Author, m.GuildID, m.ChannelID), repostAttemptTimeout)
	defer cancel()

	messageUID := m.ID
	var authorUID string
	if m.Author != nil {
		authorUID = m.Author.ID
	}
	postedAt := timestamppb.New(m.Timestamp)

	req := pb.CheckRepostReq_builder{
		Candidates: candidates,
		MessageUid: &messageUID,
		AuthorUid:  &authorUID,
		Edit:       &edit,
		PostedAt:   postedAt,
	}.Build()

	resp, err := clients.Repost.CheckRepost(ctx, connect.NewRequest(req))
	if err != nil {
		// No candidate content in the log line: URLs and attachment names are
		// the user's input, not something to persist in logs.
		log.Z.Error("failed to call CheckRepost.", zap.Error(err))
		return
	}

	matches := resp.Msg.GetMatches()
	if len(matches) == 0 {
		return
	}

	// Only the first match: a message with ten reposted images must not
	// produce ten replies.
	content := wanhaContent(matches[0])
	if content == "" {
		return
	}

	respondChat(s, &discordgo.MessageCreate{Message: m}, &command.Response{Content: content})
}

// wanhaContent renders the notification for one match: titled WANHA, a
// relative timestamp Discord renders in each viewer's own timezone
// (ADR-0018), a deep link to the original, and the original poster named via
// a suppressed mention (noMentions() stops it actually pinging).
//
// Any empty ref field degrades gracefully: a link is only built when every
// field it needs is present, and the poster falls back to a generic word
// rather than an empty `<@>` mention.
func wanhaContent(match *pb.RepostMatch) string {
	ref := match.GetOriginalRef()

	var relative string
	if match.HasOriginalPostedAt() {
		relative = " " + timestampTag(match.GetOriginalPostedAt().AsTime(), timestampRelative)
	}

	poster := "someone"
	if ref.GetAuthorUid() != "" {
		poster = "<@" + ref.GetAuthorUid() + ">"
	}

	var verb string
	switch match.GetConfidence() {
	case pb.RepostConfidence_REPOST_CONFIDENCE_HIGH:
		verb = "already posted something very similar to this"
	case pb.RepostConfidence_REPOST_CONFIDENCE_PROBABLE:
		// Worded more tentatively than the closer tiers (W9): a heavier edit
		// is weaker evidence than an identical or near-identical match.
		verb = "may have already posted something like this"
	default:
		// REPOST_CONFIDENCE_IDENTICAL, and any future tier this switch does
		// not yet know about — the safe default is the strongest wording.
		verb = "already posted this"
	}

	content := fmt.Sprintf("**WANHA** — %s %s%s", poster, verb, relative)

	if ref.GetInstanceUid() != "" && ref.GetDestinationUid() != "" && ref.GetMessageUid() != "" {
		content += fmt.Sprintf("\nhttps://discord.com/channels/%s/%s/%s",
			ref.GetInstanceUid(), ref.GetDestinationUid(), ref.GetMessageUid())
	}

	return content
}
