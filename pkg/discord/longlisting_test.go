package discord

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// longListingContent is long enough that splitContent at maxChatContent
// produces several chunks, so the delivery-path assertions below are not
// vacuously true for a listing that happens to fit in one message.
func longListingContent() string {
	return strings.Repeat("`0192f000-0000-7000-8000-000000000001` — a fairly long trigger line\n", 60)
}

func testChatMessage(channelID, authorID string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "source-message",
			ChannelID: channelID,
			Author:    &discordgo.User{ID: authorID},
		},
	}
}

// messagesTo returns every message-create call this transport observed
// against the given channel id, in order.
func messagesTo(requests []capturedRequest, channelID string) []capturedRequest {
	var out []capturedRequest
	suffix := "/channels/" + channelID + "/messages"
	for _, r := range requests {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func requestContent(r capturedRequest) string {
	content, _ := r.body["content"].(string)
	return content
}

// Every outgoing message must suppress mention parsing: a listing echoes stored
// trigger phrases and reminder text, so an @everyone in one would otherwise ping
// on the caller's behalf. An empty parse list is not the same as omitting it.
func requireMentionsSuppressed(t *testing.T, requests []capturedRequest) {
	t.Helper()

	for _, r := range requests {
		if r.method != http.MethodPost && r.method != http.MethodPatch {
			continue
		}
		if _, carriesContent := r.body["content"]; !carriesContent {
			continue
		}

		allowed, ok := r.body["allowed_mentions"].(map[string]any)
		if !ok {
			t.Errorf("%s %s: no allowed_mentions", r.method, r.path)
			continue
		}
		parse, ok := allowed["parse"].([]any)
		if !ok || len(parse) != 0 {
			t.Errorf("%s %s: allowed_mentions.parse = %v, want an empty list", r.method, r.path, allowed["parse"])
		}
	}
}

// TestChatLongListingIsDeliveredByDMWithAnInChannelPointer: content
// over maxChatContent is sent by DM in chunks, with a short pointer left in
// the channel that does not itself carry the full content.
func TestChatLongListingIsDeliveredByDMWithAnInChannelPointer(t *testing.T) {
	const dmChannelID = "dm-channel-1"
	const sourceChannelID = "chan-1"

	transport := &captureTransport{
		respond: func(req capturedRequest) routedResponse {
			if req.method == http.MethodPost && strings.HasSuffix(req.path, "/users/@me/channels") {
				return routedResponse{status: http.StatusOK, body: `{"id":"` + dmChannelID + `"}`}
			}
			return routedResponse{status: http.StatusOK, body: "{}"}
		},
	}
	s := testSessionWithTransport(transport)
	m := testChatMessage(sourceChannelID, "author-1")

	content := longListingContent()
	resp := &command.Response{Content: content, DirectWhenLong: true}

	sent := respondChat(s, m, resp)
	if sent == nil {
		t.Fatal("respondChat returned nil; the in-channel pointer was not reported as sent")
	}

	if _, ok := transport.firstOfMethod(http.MethodPost); !ok {
		t.Fatal("no request was sent at all")
	}
	dmOpened := false
	for _, r := range transport.all() {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/users/@me/channels") {
			dmOpened = true
		}
	}
	if !dmOpened {
		t.Fatal("no DM channel was opened")
	}

	dmMessages := messagesTo(transport.all(), dmChannelID)
	wantChunks := splitContent(content, maxChatContent)
	if len(dmMessages) != len(wantChunks) {
		t.Fatalf("DM messages sent = %d, want %d (one per chunk)", len(dmMessages), len(wantChunks))
	}
	for i, msg := range dmMessages {
		if got := requestContent(msg); got != wantChunks[i] {
			t.Errorf("DM chunk %d = %q, want %q", i, got, wantChunks[i])
		}
	}

	channelMessages := messagesTo(transport.all(), sourceChannelID)
	if len(channelMessages) != 1 {
		t.Fatalf("messages posted to the source channel = %d, want exactly 1 (the pointer)", len(channelMessages))
	}
	pointer := requestContent(channelMessages[0])
	if pointer == "" {
		t.Fatal("the in-channel pointer has no content")
	}
	if len(pointer) >= len(content) {
		t.Errorf("the in-channel pointer is as long as the full listing; it must not carry the content: %q", pointer)
	}
	if strings.Contains(pointer, wantChunks[0]) {
		t.Errorf("the in-channel pointer contains listing content, not just a pointer: %q", pointer)
	}

	requireMentionsSuppressed(t, transport.all())
}

// TestChatLongListingFallsBackToChannelWhenDMCannotBeOpened: if the
// DM channel cannot be opened, the caller is told in-channel and the content
// is not dumped into the channel.
func TestChatLongListingFallsBackToChannelWhenDMCannotBeOpened(t *testing.T) {
	const sourceChannelID = "chan-1"

	transport := &captureTransport{
		respond: func(req capturedRequest) routedResponse {
			if req.method == http.MethodPost && strings.HasSuffix(req.path, "/users/@me/channels") {
				return routedResponse{status: http.StatusForbidden, body: `{"message":"Cannot send messages to this user"}`}
			}
			return routedResponse{status: http.StatusOK, body: "{}"}
		},
	}
	s := testSessionWithTransport(transport)
	m := testChatMessage(sourceChannelID, "author-1")

	content := longListingContent()
	resp := &command.Response{Content: content, DirectWhenLong: true}

	sent := respondChat(s, m, resp)
	if sent == nil {
		t.Fatal("respondChat returned nil; the failure notice was not reported as sent")
	}

	channelMessages := messagesTo(transport.all(), sourceChannelID)
	if len(channelMessages) != 1 {
		t.Fatalf("messages posted to the source channel = %d, want exactly 1 (the failure notice)", len(channelMessages))
	}

	notice := requestContent(channelMessages[0])
	if len(notice) >= len(content) {
		t.Errorf("the failure notice is as long as the full listing; it must not carry the content: %q", notice)
	}
	if strings.Contains(notice, content[:100]) {
		t.Error("the failure notice contains listing content instead of just reporting the failure")
	}
}

// TestChatLongListingFallsBackToChannelWhenTheDMSendFails: the channel opens
// but sending into it fails, which must be handled the same as never opening
// it at all.
func TestChatLongListingFallsBackToChannelWhenTheDMSendFails(t *testing.T) {
	const dmChannelID = "dm-channel-1"
	const sourceChannelID = "chan-1"

	transport := &captureTransport{
		respond: func(req capturedRequest) routedResponse {
			switch {
			case req.method == http.MethodPost && strings.HasSuffix(req.path, "/users/@me/channels"):
				return routedResponse{status: http.StatusOK, body: `{"id":"` + dmChannelID + `"}`}
			case req.method == http.MethodPost && strings.HasSuffix(req.path, "/channels/"+dmChannelID+"/messages"):
				return routedResponse{status: http.StatusInternalServerError, body: `{"message":"internal"}`}
			default:
				return routedResponse{status: http.StatusOK, body: "{}"}
			}
		},
	}
	s := testSessionWithTransport(transport)
	m := testChatMessage(sourceChannelID, "author-1")

	content := longListingContent()
	resp := &command.Response{Content: content, DirectWhenLong: true}

	respondChat(s, m, resp)

	channelMessages := messagesTo(transport.all(), sourceChannelID)
	if len(channelMessages) != 1 {
		t.Fatalf("messages posted to the source channel = %d, want exactly 1 (the failure notice)", len(channelMessages))
	}
	if notice := requestContent(channelMessages[0]); len(notice) >= len(content) {
		t.Errorf("the failure notice is as long as the full listing; it must not carry the content: %q", notice)
	}
}

// TestChatShortListingIsNotSentByDM: DirectWhenLong must not divert content
// that already fits in one message.
func TestChatShortListingIsNotSentByDM(t *testing.T) {
	transport := &captureTransport{}
	s := testSessionWithTransport(transport)
	m := testChatMessage("chan-1", "author-1")

	resp := &command.Response{Content: "short listing", DirectWhenLong: true}
	respondChat(s, m, resp)

	for _, r := range transport.all() {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/users/@me/channels") {
			t.Error("a short DirectWhenLong response still opened a DM channel")
		}
	}

	channelMessages := messagesTo(transport.all(), "chan-1")
	if len(channelMessages) != 1 || requestContent(channelMessages[0]) != "short listing" {
		t.Errorf("channel messages = %+v, want exactly the short content unchanged", channelMessages)
	}
}

// TestSlashLongListingArrivesAsFollowUpsWithTheSameVisibility: a
// slash listing over the limit is not truncated; the remainder arrives as
// follow-up messages carrying the deferral's own visibility.
func TestSlashLongListingArrivesAsFollowUpsWithTheSameVisibility(t *testing.T) {
	tests := []struct {
		name      string
		ephemeral bool
	}{
		{name: "public deferral, public follow-ups", ephemeral: false},
		{name: "ephemeral deferral, ephemeral follow-ups", ephemeral: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &captureTransport{}
			s := testSessionWithTransport(transport)
			i := testSlashInteraction()

			content := longListingContent()
			cmd := command.Command{
				Name:      "slow-listing",
				Slow:      true,
				Ephemeral: tt.ephemeral,
				Handler: func(_ context.Context, _ *command.Invocation) (*command.Response, error) {
					return &command.Response{Content: content}, nil
				},
			}
			inv, err := command.Bind(cmd, nil)
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}

			runInteraction(s, i, cmd, inv, &client.Clients{})

			editReq, ok := transport.firstOfMethod(http.MethodPatch)
			if !ok {
				t.Fatal("no edit (first chunk) request was sent")
			}

			var followups []capturedRequest
			for _, r := range transport.all() {
				if r.method == http.MethodPost && strings.Contains(r.path, "/webhooks/") {
					followups = append(followups, r)
				}
			}

			wantChunks := splitContent(content, maxChatContent)
			if len(followups) != len(wantChunks)-1 {
				t.Fatalf("follow-up messages = %d, want %d (every chunk after the first edit)",
					len(followups), len(wantChunks)-1)
			}

			got := requestContent(editReq)
			for _, f := range followups {
				got += requestContent(f)
			}
			want := strings.Join(wantChunks, "")
			if got != want {
				t.Errorf("reassembled delivered content does not match the original listing")
			}

			for i, f := range followups {
				isEphemeral := topLevelEphemeral(t, f)
				if isEphemeral != tt.ephemeral {
					t.Errorf("follow-up %d ephemeral = %v, want %v (matching the deferral)", i, isEphemeral, tt.ephemeral)
				}
			}

			requireMentionsSuppressed(t, transport.all())
		})
	}
}

// topLevelEphemeral reads the ephemeral flag from a WebhookParams-shaped body
// (a follow-up message), whose flags field is top-level rather than nested
// under "data" the way an interaction callback's is.
func topLevelEphemeral(t *testing.T, req capturedRequest) bool {
	t.Helper()

	flags, ok := req.body["flags"]
	if !ok {
		return false
	}

	value, ok := flags.(float64)
	if !ok {
		t.Fatalf("flags is not a number: %#v", flags)
	}

	return int(value)&int(discordgo.MessageFlagsEphemeral) != 0
}
