package discord

import (
	"strings"
	"testing"
	"unicode/utf8"

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

// TestReRollComponents covers the update path: a response that wants no button
// must produce an empty, non-nil slice, because nil leaves the existing
// components in place when a message is edited rather than replaced.
func TestReRollComponents(t *testing.T) {
	withButton := reRollComponents(&command.Response{ReRollID: "reroll:doubles"})
	if len(withButton) != 1 {
		t.Errorf("a response asking for a re-roll produced %d components, want 1", len(withButton))
	}

	withoutButton := reRollComponents(&command.Response{})
	if withoutButton == nil {
		t.Error("a response with no re-roll produced nil, which would not clear an existing button")
	}
	if len(withoutButton) != 0 {
		t.Errorf("a response with no re-roll produced %d components, want 0", len(withoutButton))
	}
}
