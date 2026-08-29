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

// TestErrorMessage pins which Connect codes reach a channel verbatim.
// InvalidArgument and FailedPrecondition are for the caller; everything else is
// replaced so internal detail cannot leak into a public guild.
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
			// A non-Connect error is CodeUnknown, which must fall through generic.
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

// TestUnavailableIsNotTheGenericMessage pins that a transient outage is
// distinguishable from the generic message, whatever the wording becomes.
func TestUnavailableIsNotTheGenericMessage(t *testing.T) {
	generic := errorMessage(errors.New("some ordinary Go error"))

	unavailable := errorMessage(connect.NewError(connect.CodeUnavailable, errors.New("connection refused")))
	if unavailable == generic {
		t.Errorf("errorMessage(Unavailable) = %q, which is the generic message; "+
			"a transient outage must be distinguishable from an unexplained failure", unavailable)
	}
}

// TestErrorMessageDoesNotLeakTheConnectCodePrefix: connect.Error.Error() renders
// as "invalid_argument: <message>", so errorMessage must read Message(), not Error().
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

// TestTruncateContent: Discord rejects a send above the limit, and cutting
// mid-rune would produce invalid UTF-8.
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

// TestNoMentionsParsesNothing: an empty Parse list suppresses all mentions,
// whereas omitting AllowedMentions resolves them all — a nil slice reopens the hole.
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
// A button click is acknowledged without editing the clicked message.
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

// TestReRollButtonStopsAfterOneHop: a slash or chat roll carries the button; the
// roll from clicking it must not, or every click grows a chain.
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

// TestClickedMessageReferencePointsAtTheClickedMessage: Interaction.Message is
// only set for a component interaction, so the absent case must not deref nil.
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

// readAttachment reads a planned attachment back to tell it from a header with
// an exhausted reader.
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

// TestResponseFilesRefusesEveryEmptyShape: each empty shape would otherwise post
// a zero-byte attachment named "", an undeletable artefact in the channel.
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

// TestResponseFilesCarriesTheNameTypeAndBytes maps the neutral response onto
// discordgo's attachment; a blank name is ordinary and gets the fallback.
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

// TestResponseFilesGivesAFreshReaderPerCall: an io.Reader is consumed once, and
// respondCommand can send from one response twice.
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

// TestPlanResponseCarriesTheFileOnEveryPath: the file must ride every one of the
// three delivery paths, which deliver differently.
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

			textOnly := planResponse(source.source, &command.Response{Content: "444"})
			if len(textOnly.files) != 0 {
				t.Errorf("a text-only response planned %d attachments, want 0", len(textOnly.files))
			}
		})
	}
}
