package discord

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestErrorMessage pins which gRPC codes are allowed to reach a channel
// verbatim. InvalidArgument and FailedPrecondition are written for the caller;
// everything else is internal and must be replaced, so that a database error or
// a stack detail cannot be echoed into a public guild.
func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "invalid argument reaches the caller",
			err:  status.Error(codes.InvalidArgument, "lower must be a whole number"),
			want: "lower must be a whole number",
		},
		{
			name: "failed precondition reaches the caller",
			err:  status.Error(codes.FailedPrecondition, "you are not registered"),
			want: "you are not registered",
		},
		{
			name: "permission denied is replaced",
			err:  status.Error(codes.PermissionDenied, "user 42 lacks clearance 20"),
			want: "You are not allowed to do that.",
		},
		{
			name: "not found is replaced",
			err:  status.Error(codes.NotFound, "reminder 7 not in table reminder"),
			want: "Not found.",
		},
		{
			name: "unimplemented is replaced",
			err:  status.Error(codes.Unimplemented, "method Ping not implemented"),
			want: "That is not implemented yet.",
		},
		{
			name: "internal detail never leaks",
			err:  status.Error(codes.Internal, `pgx: connection refused to 10.0.0.5:5432`),
			want: "Something went wrong.",
		},
		{
			name: "unavailable never leaks",
			err:  status.Error(codes.Unavailable, "grpc: no connection to ginbot-server:50051"),
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
