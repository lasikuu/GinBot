package discord

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
)

// TestErrorMessage pins which Connect codes are allowed to reach a channel
// verbatim. InvalidArgument and FailedPrecondition are written for the caller;
// everything else is internal and must be replaced, so that a database error or
// a stack detail cannot be echoed into a public guild.
//
// errorMessage switched from google.golang.org/grpc/status.FromError to
// connect.CodeOf as part of the Connect port, and gained a sixth branch for
// connect.CodeUnavailable — the code a client gets when it cannot reach
// ginbot-server at all, which previously fell through to the same generic
// message as codes.Unavailable did. That branch's wording is asserted
// alongside the five pre-existing ones so a future edit cannot special-case
// Unavailable without the others noticing, and cannot leave Unavailable
// producing the bare fallback without ITS case noticing either.
func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "invalid argument reaches the caller",
			err:  connect.NewError(connect.CodeInvalidArgument, errors.New("lower must be a whole number")),
			want: "lower must be a whole number",
		},
		{
			name: "failed precondition reaches the caller",
			err:  connect.NewError(connect.CodeFailedPrecondition, errors.New("you are not registered")),
			want: "you are not registered",
		},
		{
			name: "permission denied is replaced",
			err:  connect.NewError(connect.CodePermissionDenied, errors.New("user 42 lacks clearance 20")),
			want: "You are not allowed to do that.",
		},
		{
			name: "not found is replaced",
			err:  connect.NewError(connect.CodeNotFound, errors.New("reminder 7 not in table reminder")),
			want: "Not found.",
		},
		{
			name: "unimplemented is replaced",
			err:  connect.NewError(connect.CodeUnimplemented, errors.New("method Ping not implemented")),
			want: "That is not implemented yet.",
		},
		{
			name: "internal detail never leaks",
			err:  connect.NewError(connect.CodeInternal, errors.New(`pgx: connection refused to 10.0.0.5:5432`)),
			want: "Something went wrong.",
		},
		{
			// The sixth branch. Two things at once: the transport error
			// (connection refused, DNS failure, ...) must not reach a public
			// channel, AND the caller must be told this one is transient —
			// which is the only failure here they can usefully act on.
			// TestUnavailableIsNotTheGenericMessage below is what stops this
			// silently collapsing back into the default.
			name: "unavailable gets its own actionable message",
			err:  connect.NewError(connect.CodeUnavailable, errors.New("dial tcp 10.0.0.9:50051: connect: connection refused")),
			want: "The bot's backend is unreachable right now. Try again in a moment.",
		},
		{
			name: "nil error is still a generic message",
			err:  nil,
			want: "Something went wrong.",
		},
		{
			// Not every error reaching errorMessage originated as a Connect
			// error — a bare error from local validation, or something a
			// dependency returned unwrapped. connect.CodeOf reports
			// CodeUnknown for these, which must fall through to the generic
			// message rather than panic or leak the raw error text.
			name: "a non-Connect error is still a generic message",
			err:  errors.New("some ordinary Go error"),
			want: "Something went wrong.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorMessage(tt.err); got != tt.want {
				t.Errorf("errorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUnavailableIsNotTheGenericMessage pins the POINT of the sixth branch
// rather than its wording.
//
// An earlier version of errorMessage had the CodeUnavailable case assign the
// same string as the default, on the reasoning that the case's existence was
// itself the safeguard. It was not: deleting the whole case changed no
// observable behaviour and the entire package's tests stayed green, so the
// branch was inert and the requirement it was meant to satisfy — a transient
// backend outage must not reach the user as "Something went wrong." — was
// still unmet. Comparing against the generic message rather than a literal is
// what makes that impossible to reintroduce, whatever the wording becomes.
func TestUnavailableIsNotTheGenericMessage(t *testing.T) {
	generic := errorMessage(errors.New("some ordinary Go error"))

	unavailable := errorMessage(connect.NewError(connect.CodeUnavailable, errors.New("connection refused")))
	if unavailable == generic {
		t.Errorf("errorMessage(Unavailable) = %q, which is the generic message; "+
			"a transient outage must be distinguishable from an unexplained failure", unavailable)
	}
}

// TestErrorMessageDoesNotLeakTheConnectCodePrefix guards against a regression
// specific to connect.Error: its Error() method (and therefore fmt formatting
// of the error) renders as "invalid_argument: <message>", prefixing the code
// name onto the text. errorMessage must read st.Message() through the typed
// accessor — never err.Error() — or every InvalidArgument/FailedPrecondition
// reply shown to a Discord user would carry a leaked "invalid_argument: "
// prefix that means nothing to them.
func TestErrorMessageDoesNotLeakTheConnectCodePrefix(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"invalid argument", connect.NewError(connect.CodeInvalidArgument, errors.New("lower must be a whole number"))},
		{"failed precondition", connect.NewError(connect.CodeFailedPrecondition, errors.New("you are not registered"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorMessage(tt.err)
			for _, code := range []string{"invalid_argument", "failed_precondition"} {
				if strings.Contains(got, code) {
					t.Errorf("errorMessage() = %q, leaks the Connect code prefix %q", got, code)
				}
			}
		})
	}
}

// TestTruncateContent guards the cut. Discord rejects a send above the limit
// outright, so an over-long echoed argument would otherwise produce no reply at
// all, and cutting mid-rune would produce invalid UTF-8.
func TestTruncateContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "short", content: "hello"},
		{name: "exactly at the limit", content: strings.Repeat("a", maxChatContent)},
		{name: "one over the limit", content: strings.Repeat("a", maxChatContent+1)},
		{name: "far over the limit", content: strings.Repeat("a", maxChatContent*3)},
		{name: "multi-byte runes over the limit", content: strings.Repeat("ä", maxChatContent)},
		{name: "four-byte runes over the limit", content: strings.Repeat("🎲", maxChatContent)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateContent(tt.content)

			if len(got) > maxChatContent {
				t.Errorf("length = %d, want at most %d", len(got), maxChatContent)
			}
			if !utf8.ValidString(got) {
				t.Error("result is not valid UTF-8")
			}
			if len(tt.content) <= maxChatContent && got != tt.content {
				t.Errorf("content within the limit was modified: %q", got)
			}
		})
	}
}

// TestNoMentionsParsesNothing pins the distinction the mention-injection fix
// rests on: an empty Parse list means "resolve no mentions", whereas omitting
// AllowedMentions entirely means "resolve all of them". A nil slice here would
// silently restore the hole.
func TestNoMentionsParsesNothing(t *testing.T) {
	allowed := noMentions()

	if allowed == nil {
		t.Fatal("noMentions() is nil, which lets Discord parse every mention")
	}
	if allowed.Parse == nil {
		t.Fatal("Parse is nil; it must be an empty slice to suppress mentions")
	}
	if len(allowed.Parse) != 0 {
		t.Errorf("Parse = %v, want empty", allowed.Parse)
	}
	if len(allowed.Users) != 0 || len(allowed.Roles) != 0 {
		t.Errorf("Users = %v, Roles = %v, want both empty", allowed.Users, allowed.Roles)
	}
}

// TestReRollComponents covers the mapping from a response onto components: a
// response asking for a re-roll gets exactly one control, one that does not
// gets none.
func TestReRollComponents(t *testing.T) {
	withButton := reRollComponents(&command.Response{ReRollID: "reroll:doubles"})
	if len(withButton) != 1 {
		t.Errorf("a response asking for a re-roll produced %d components, want 1", len(withButton))
	}

	if withoutButton := reRollComponents(&command.Response{}); len(withoutButton) != 0 {
		t.Errorf("a response with no re-roll produced %d components, want 0", len(withoutButton))
	}
}

// TestPlanResponse pins the delivery decision for all three invocation paths.
// It is the whole of the response path that can be checked directly: the two
// API calls it drives need a discordgo.Session and there is no fake for one.
//
// The re-roll row is the correction. The old code answered a button click with
// InteractionResponseUpdateMessage, which rewrote the clicked message in place
// and re-attached the button. A click must instead be acknowledged without
// touching that message, and the roll posted separately.
func TestPlanResponse(t *testing.T) {
	rolled := &command.Response{Content: "444", ReRollID: reRollID("triples")}

	tests := []struct {
		name           string
		source         commandSource
		resp           *command.Response
		wantResponse   discordgo.InteractionResponseType
		wantReply      bool
		wantComponents int
	}{
		{
			name:           "a slash command answers with the content",
			source:         sourceSlash,
			resp:           rolled,
			wantResponse:   discordgo.InteractionResponseChannelMessageWithSource,
			wantReply:      false,
			wantComponents: 1,
		},
		{
			name:           "a chat command has no interaction to answer",
			source:         sourceChat,
			resp:           rolled,
			wantResponse:   interactionNone,
			wantReply:      true,
			wantComponents: 1,
		},
		{
			name:           "a button click is acknowledged without editing the clicked message",
			source:         sourceReRoll,
			resp:           rolled,
			wantResponse:   discordgo.InteractionResponseDeferredMessageUpdate,
			wantReply:      true,
			wantComponents: 0,
		},
		{
			name:           "a response that asks for no re-roll gets no control",
			source:         sourceSlash,
			resp:           &command.Response{Content: "7"},
			wantResponse:   discordgo.InteractionResponseChannelMessageWithSource,
			wantReply:      false,
			wantComponents: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planResponse(tt.source, tt.resp)

			if plan.interactionResponse == discordgo.InteractionResponseUpdateMessage {
				t.Error("plan edits the clicked message in place")
			}
			if plan.interactionResponse != tt.wantResponse {
				t.Errorf("interactionResponse = %d, want %d", plan.interactionResponse, tt.wantResponse)
			}
			if plan.replyInChannel != tt.wantReply {
				t.Errorf("replyInChannel = %v, want %v", plan.replyInChannel, tt.wantReply)
			}
			if len(plan.components) != tt.wantComponents {
				t.Errorf("components = %d, want %d", len(plan.components), tt.wantComponents)
			}
		})
	}
}

// TestReRollButtonStopsAfterOneHop is the regression the correction is about.
// A roll invoked as a slash or chat command carries the die button; the roll
// produced by clicking that button must not, or every click grows a chain.
//
// The button is identified by its custom ID rather than by counting, so a
// component that is merely present but not a re-roll control cannot pass.
func TestReRollButtonStopsAfterOneHop(t *testing.T) {
	resp := &command.Response{Content: "444", ReRollID: reRollID("triples")}

	tests := []struct {
		name       string
		source     commandSource
		wantButton bool
	}{
		{name: "slash invocation", source: sourceSlash, wantButton: true},
		{name: "chat invocation", source: sourceChat, wantButton: true},
		{name: "button click", source: sourceReRoll, wantButton: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasReRollButton(planResponse(tt.source, resp).components, resp.ReRollID)
			if got != tt.wantButton {
				t.Errorf("re-roll button present = %v, want %v", got, tt.wantButton)
			}
		})
	}
}

// hasReRollButton reports whether the components carry a button bound to
// customID, which is what makes a message clickable again.
func hasReRollButton(components []discordgo.MessageComponent, customID string) bool {
	for _, component := range components {
		row, ok := component.(*discordgo.ActionsRow)
		if !ok {
			continue
		}

		for _, nested := range row.Components {
			if button, ok := nested.(discordgo.Button); ok && button.CustomID == customID {
				return true
			}
		}
	}

	return false
}

// TestClickedMessageReferencePointsAtTheClickedMessage covers the reply target.
// Interaction.Message is only populated for a component interaction, and
// discordgo dispatches handlers without recovering panics, so the absent case
// must degrade to no reference rather than deref nil.
func TestClickedMessageReferencePointsAtTheClickedMessage(t *testing.T) {
	clicked := &discordgo.Message{ID: "msg", ChannelID: "chan", GuildID: "guild"}

	reference := clickedMessageReference(&discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{ChannelID: "chan", Message: clicked},
	})
	if reference == nil {
		t.Fatal("no reference for a clicked message; the roll would not be a reply")
	}
	if reference.MessageID != clicked.ID {
		t.Errorf("MessageID = %q, want %q", reference.MessageID, clicked.ID)
	}
	if reference.ChannelID != clicked.ChannelID {
		t.Errorf("ChannelID = %q, want %q", reference.ChannelID, clicked.ChannelID)
	}

	absent := clickedMessageReference(&discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{ChannelID: "chan"},
	})
	if absent != nil {
		t.Errorf("reference = %+v for an interaction with no message, want nil", absent)
	}
}

// responseWithFile is a response carrying one attachment, i.e. what a trigger
// whose reply is a file produces.
func responseWithFile(name string, content []byte) *command.Response {
	return &command.Response{
		Content: "",
		File: &command.ResponseFile{
			Name:     name,
			MIMEType: "image/png",
			Content:  content,
		},
	}
}

// readAttachment reads a planned attachment back, which is the only way to tell
// a real attachment from a header with an exhausted reader behind it.
func readAttachment(t *testing.T, file *discordgo.File) []byte {
	t.Helper()

	if file.Reader == nil {
		t.Fatal("attachment has no reader; discordgo would post an empty file")
	}
	content, err := io.ReadAll(file.Reader)
	if err != nil {
		t.Fatalf("read attachment %q: %v", file.Name, err)
	}

	return content
}

// TestResponseFilesRefusesEveryEmptyShape: no code in this repository sent a
// file to a platform before this, so all of these reach discordgo for the first
// time. Each one would otherwise post a zero-byte attachment named "" — a
// visible, undeletable artefact in the channel — and the nil Response is the
// shape respondCommand already has to handle, because a handler is free to
// return (nil, nil).
func TestResponseFilesRefusesEveryEmptyShape(t *testing.T) {
	tests := []struct {
		name string
		resp *command.Response
	}{
		{name: "nil response", resp: nil},
		{name: "a response with no file", resp: &command.Response{Content: "444"}},
		{name: "a file with nil content", resp: responseWithFile("cat.png", nil)},
		{name: "a file with empty content", resp: responseWithFile("cat.png", []byte{})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responseFiles(tt.resp); got != nil {
				t.Errorf("responseFiles() = %+v, want nil", got)
			}
		})
	}
}

// TestResponseFilesCarriesTheNameTypeAndBytes is the mapping from the neutral
// response onto discordgo's attachment.
//
// The name is what the user sees and what their client saves the file as; the
// server never stores an original filename, so a blank one is the ordinary case
// rather than an error, and Discord rejects an attachment with no name at all.
// ContentType is what decides whether Discord renders the file inline or as a
// download.
func TestResponseFilesCarriesTheNameTypeAndBytes(t *testing.T) {
	content := []byte("\x89PNG not really")

	tests := []struct {
		name     string
		given    string
		wantName string
	}{
		{name: "a named file keeps its name", given: "cat.png", wantName: "cat.png"},
		{name: "a nameless file gets the fallback", given: "", wantName: fallbackAttachmentName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := responseFiles(responseWithFile(tt.given, content))
			if len(files) != 1 {
				t.Fatalf("responseFiles() produced %d attachments, want 1", len(files))
			}

			file := files[0]
			if file.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", file.Name, tt.wantName)
			}
			if file.ContentType != "image/png" {
				t.Errorf("ContentType = %q, want %q", file.ContentType, "image/png")
			}
			if got := readAttachment(t, file); !bytes.Equal(got, content) {
				t.Errorf("attachment content = %q, want %q", got, content)
			}
		})
	}
}

// TestResponseFilesGivesAFreshReaderPerCall: an io.Reader is consumed once, so a
// reader cached alongside the response would be empty the second time it was
// used. That is not hypothetical — respondCommand plans a response and can both
// answer the interaction and send a channel message from it, and a send that
// discordgo retries would replay the same reader.
func TestResponseFilesGivesAFreshReaderPerCall(t *testing.T) {
	content := []byte("the same bytes twice")
	resp := responseWithFile("cat.png", content)

	for attempt := 1; attempt <= 2; attempt++ {
		files := responseFiles(resp)
		if len(files) != 1 {
			t.Fatalf("attempt %d produced %d attachments, want 1", attempt, len(files))
		}
		if got := readAttachment(t, files[0]); !bytes.Equal(got, content) {
			t.Errorf("attempt %d read %q, want %q", attempt, got, content)
		}
	}
}

// TestPlanResponseCarriesTheFileOnEveryPath is the acceptance criterion that a
// file reply arrives as a real attachment, reduced to the part that can be
// checked without a Discord session.
//
// All three sources matter and they deliver differently: a slash command's file
// rides the interaction callback, a chat command's rides a channel message, and
// a re-roll acknowledges the click and then sends a separate reply. Wiring the
// attachment into only the one path that was tried by hand is the obvious way to
// get this wrong.
func TestPlanResponseCarriesTheFileOnEveryPath(t *testing.T) {
	content := []byte("attach me")

	for _, source := range []struct {
		name   string
		source commandSource
	}{
		{name: "slash", source: sourceSlash},
		{name: "chat", source: sourceChat},
		{name: "re-roll", source: sourceReRoll},
	} {
		t.Run(source.name, func(t *testing.T) {
			plan := planResponse(source.source, responseWithFile("cat.png", content))
			if len(plan.files) != 1 {
				t.Fatalf("plan carries %d attachments, want 1", len(plan.files))
			}
			if plan.files[0].Name != "cat.png" {
				t.Errorf("Name = %q, want %q", plan.files[0].Name, "cat.png")
			}
			if got := readAttachment(t, plan.files[0]); !bytes.Equal(got, content) {
				t.Errorf("attachment content = %q, want %q", got, content)
			}

			// A response with no file must not grow one, or every text reply
			// would carry an empty attachment.
			textOnly := planResponse(source.source, &command.Response{Content: "444"})
			if len(textOnly.files) != 0 {
				t.Errorf("a text-only response planned %d attachments, want 0", len(textOnly.files))
			}
		})
	}
}
