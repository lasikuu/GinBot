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

// maxRepostCandidates mirrors CheckRepostReq.candidates' buf.validate max_items=20.
const maxRepostCandidates = 20

const repostAttemptTimeout = 15 * time.Second

const maxConcurrentRepostAttempts = 4

// repostAttemptSlots caps in-flight checks; acquisition is non-blocking so a
// check is dropped under load rather than queued and posted late.
var repostAttemptSlots = make(chan struct{}, maxConcurrentRepostAttempts)

// repostCandidates extracts links then attachments, capped at
// maxRepostCandidates; links come first so they survive the cap.
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

// repostAttachmentKind is a routing hint only; the server re-derives the
// authoritative kind from the fetched bytes. ContentType is preferred, with the
// filename extension as fallback.
func repostAttachmentKind(a *discordgo.MessageAttachment) pb.RepostKind {
	if a.ContentType != "" {
		return repost.KindFromContentType(a.ContentType)
	}
	return repost.KindFromFilename(a.Filename)
}

// attemptRepost offers one message to the server and posts WANHA for anything
// already seen. Identity and origin travel as metadata; any failure says
// nothing rather than commenting on ordinary conversation.
func attemptRepost(s *discordgo.Session, m *discordgo.Message, edit bool, clients *client.Clients) {
	if m == nil || m.GuildID == "" {
		// A repost only exists within the guild that saw the original; the
		// server refuses a DM with FailedPrecondition.
		return
	}

	candidates := repostCandidates(m)
	if len(candidates) == 0 {
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
		// No candidate content logged: URLs and attachment names are user input.
		log.Z.Error("failed to call CheckRepost.", zap.Error(err))
		return
	}

	matches := resp.Msg.GetMatches()
	if len(matches) == 0 {
		return
	}

	// Only the first match, so ten reposted images do not produce ten replies.
	content := wanhaContent(matches[0])
	if content == "" {
		return
	}

	respondChat(s, &discordgo.MessageCreate{Message: m}, &command.Response{Content: content})
}

// wanhaContent renders the notification for one match. Empty ref fields degrade
// gracefully: the link is built only when complete, the poster falls back to a
// generic word rather than an empty mention.
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
		verb = "may have already posted something like this"
	default:
		// IDENTICAL and any future tier: the safe default is the strongest wording.
		verb = "already posted this"
	}

	content := fmt.Sprintf("**WANHA** — %s %s%s", poster, verb, relative)

	if ref.GetInstanceUid() != "" && ref.GetDestinationUid() != "" && ref.GetMessageUid() != "" {
		content += fmt.Sprintf("\nhttps://discord.com/channels/%s/%s/%s",
			ref.GetInstanceUid(), ref.GetDestinationUid(), ref.GetMessageUid())
	}

	return content
}
