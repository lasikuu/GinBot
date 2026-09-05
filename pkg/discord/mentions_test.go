package discord

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/trigger"
)

// TestStripSelfMention covers every position, the legacy nickname form, and
// that only the given id's mention is removed: a trigger phrase must survive
// untouched if it is not the bot's own mention.
func TestStripSelfMention(t *testing.T) {
	tests := []struct {
		name    string
		content string
		id      string
		want    string
	}{
		{
			// The token itself is removed; the space that followed it is not
			// trimmed here; StripSpoilers (run downstream by trigger.Select)
			// collapses and trims whitespace before matching.
			name:    "mention at the start",
			content: "<@123> ok",
			id:      "123",
			want:    " ok",
		},
		{
			name:    "legacy nickname mention at the start",
			content: "<@!123> ok",
			id:      "123",
			want:    " ok",
		},
		{
			name:    "mention in the middle",
			content: "hey <@123> how are you",
			id:      "123",
			want:    "hey  how are you",
		},
		{
			name:    "mention at the end",
			content: "ok <@123>",
			id:      "123",
			want:    "ok ",
		},
		{
			name:    "repeated mentions",
			content: "<@123> <@123> ok <@123>",
			id:      "123",
			want:    "  ok ",
		},
		{
			name:    "another user's mention is left alone",
			content: "<@456> ok",
			id:      "123",
			want:    "<@456> ok",
		},
		{
			name:    "a role mention is left alone",
			content: "<@&123> ok",
			id:      "123",
			want:    "<@&123> ok",
		},
		{
			name:    "a channel mention is left alone",
			content: "<#123> ok",
			id:      "123",
			want:    "<#123> ok",
		},
		{
			name:    "no mention present is a no-op",
			content: "just ok",
			id:      "123",
			want:    "just ok",
		},
		{
			name:    "an empty id is a no-op",
			content: "<@123> ok",
			id:      "",
			want:    "<@123> ok",
		},
		{
			name:    "empty content",
			content: "",
			id:      "123",
			want:    "",
		},
		{
			name:    "only the mention",
			content: "<@123>",
			id:      "123",
			want:    "",
		},
		{
			name:    "legacy and modern forms mixed",
			content: "<@!123> hi <@123>",
			id:      "123",
			want:    " hi ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripSelfMention(tt.content, tt.id); got != tt.want {
				t.Errorf("stripSelfMention(%q, %q) = %q, want %q", tt.content, tt.id, got, tt.want)
			}
		})
	}
}

// exactCandidate compiles a single exact-mode candidate against pkg/trigger's
// real matcher, so the end-to-end assertions below exercise the same code path
// production trigger matching does.
func exactCandidate(t *testing.T, id string, phrase string) trigger.Candidate {
	t.Helper()

	pattern, err := trigger.Compile(phrase, pb.TriggerMode_TRIGGER_MODE_EXACT)
	if err != nil {
		t.Fatalf("trigger.Compile(%q, EXACT): %v", phrase, err)
	}

	return trigger.Candidate{ID: id, Mode: pb.TriggerMode_TRIGGER_MODE_EXACT, Chance: 100, Pattern: pattern}
}

func anyCandidate(t *testing.T, id string, phrase string) trigger.Candidate {
	t.Helper()

	pattern, err := trigger.Compile(phrase, pb.TriggerMode_TRIGGER_MODE_ANY)
	if err != nil {
		t.Fatalf("trigger.Compile(%q, ANY): %v", phrase, err)
	}

	return trigger.Candidate{ID: id, Mode: pb.TriggerMode_TRIGGER_MODE_ANY, Chance: 100, Pattern: pattern}
}

func alwaysZero(int) int { return 0 }

// matchPhrase mirrors what attemptTrigger sends the server: the self-mention
// stripped, then the server's own Select/Compile/StripSpoilers pipeline.
func matchPhrase(botID, content string, candidates []trigger.Candidate) *trigger.Candidate {
	phrase := stripSelfMention(content, botID)
	return trigger.Select(phrase, candidates, alwaysZero)
}

// TestExactModeFiresAfterStripSelfMention is the end-to-end regression test
// for the bug described in the brief: an exact-mode trigger fires when the
// bot is mentioned before the phrase, and still fires for the bare phrase.
func TestExactModeFiresAfterStripSelfMention(t *testing.T) {
	candidates := []trigger.Candidate{exactCandidate(t, "trig-1", "ok")}

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "mention then phrase", content: "<@123> ok", want: true},
		{name: "phrase alone", content: "ok", want: true},
		{name: "a superstring is not an exact match", content: "okay", want: false},
		{name: "the phrase embedded in a sentence is not exact", content: "that is ok", want: false},
		{name: "legacy nickname mention", content: "<@!123> ok", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPhrase("123", tt.content, candidates)
			if fired := got != nil; fired != tt.want {
				t.Errorf("matchPhrase(%q) fired = %v, want %v (got %+v)", tt.content, fired, tt.want, got)
			}
		})
	}
}

// TestAnyModeIsUnaffectedBySelfMentionStripping: any-mode already matched a
// mention-prefixed message via its own word boundary, and must keep matching
// once the mention is stripped instead.
func TestAnyModeIsUnaffectedBySelfMentionStripping(t *testing.T) {
	candidates := []trigger.Candidate{anyCandidate(t, "trig-any", "ok")}

	for _, content := range []string{"<@123> ok", "ok", "well ok then", "<@123> well ok then"} {
		if got := matchPhrase("123", content, candidates); got == nil {
			t.Errorf("matchPhrase(%q) did not fire the any-mode trigger", content)
		}
	}
}

// TestMentionOnlyMessageYieldsAnEmptyPhrase: the server's TryTrigger refuses an
// empty phrase with InvalidArgument, so a mention-only message must reduce to
// empty before ever reaching the RPC, never a client-side special case.
func TestMentionOnlyMessageYieldsAnEmptyPhrase(t *testing.T) {
	if got := stripSelfMention("<@123>", "123"); got != "" {
		t.Errorf("stripSelfMention(mention only) = %q, want empty", got)
	}
}

// TestForcedIsDecidedFromTheOriginalContent: mentionsBot must see the raw
// message, since attemptTrigger decides "forced" before any stripping happens.
// A mention-only-plus-phrase message is still a forced fire.
func TestForcedIsDecidedFromTheOriginalContent(t *testing.T) {
	s := sessionAs("123")
	m := messageMentioning(&discordgo.User{ID: "123"})
	m.Content = "<@123> ok"

	if !mentionsBot(s, m) {
		t.Fatal("mentionsBot did not see the mention in the original content")
	}

	// The phrase sent onward is still reduced, independent of the forced
	// decision; StripSpoilers collapses the resulting whitespace downstream.
	if got := strings.TrimSpace(stripSelfMention(m.Content, "123")); got != "ok" {
		t.Errorf("stripSelfMention(%q, %q) trimmed = %q, want %q", m.Content, "123", got, "ok")
	}
}

// TestExactModeHandlesNonASCIIPhrases: Go's regexp \b is ASCII-only, so a
// Finnish phrase must not rely on it. Exact mode's anchors are not word
// boundaries at all, so this is a regression guard against a future change
// that tries to reuse the any-mode boundary logic for exact mode too.
func TestExactModeHandlesNonASCIIPhrases(t *testing.T) {
	candidates := []trigger.Candidate{exactCandidate(t, "trig-fi", "selvä")}

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "mention then the Finnish phrase", content: "<@123> selvä", want: true},
		{name: "the Finnish phrase alone", content: "selvä", want: true},
		{name: "a superstring is not exact", content: "selvästi", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPhrase("123", tt.content, candidates)
			if fired := got != nil; fired != tt.want {
				t.Errorf("matchPhrase(%q) fired = %v, want %v", tt.content, fired, tt.want)
			}
		})
	}
}

// fakeTryTriggerClient is a minimal, self-contained fake covering only
// TryTrigger, so this file's attemptTrigger tests do not depend on the
// larger fakeTriggerClient defined for the trigger-command tests.
type fakeTryTriggerClient struct {
	ginbotv1connect.TriggerServiceClient

	calls   int
	lastReq *pb.TryTriggerReq
}

func (f *fakeTryTriggerClient) TryTrigger(_ context.Context, in *connect.Request[pb.TryTriggerReq]) (*connect.Response[pb.TryTriggerResp], error) {
	f.calls++
	f.lastReq = in.Msg
	return connect.NewResponse(pb.TryTriggerResp_builder{}.Build()), nil
}

func triggerMessage(guildID, channelID, authorID, content string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			GuildID:   guildID,
			ChannelID: channelID,
			Content:   content,
			Author:    &discordgo.User{ID: authorID},
		},
	}
}

// TestAttemptTriggerSkipsTheRPCForAMentionOnlyMessage: the server
// refuses an empty phrase with InvalidArgument, so a mention-only message must
// never reach TryTrigger at all.
func TestAttemptTriggerSkipsTheRPCForAMentionOnlyMessage(t *testing.T) {
	fake := &fakeTryTriggerClient{}
	s := sessionAs("bot-1")
	m := triggerMessage("guild-1", "chan-1", "author-1", "<@bot-1>")

	attemptTrigger(s, m, true, &client.Clients{Trigger: fake})

	if fake.calls != 0 {
		t.Errorf("TryTrigger was called %d times for a mention-only message, want 0", fake.calls)
	}
}

// TestAttemptTriggerSendsTheForcedFlagAndTheStrippedPhrase composes
// mentionsBot (decided from the original content) with attemptTrigger the way
// handleMessage does, and checks both halves of the fix land in the request:
// forced is still true, and the phrase sent onward had the mention removed.
func TestAttemptTriggerSendsTheForcedFlagAndTheStrippedPhrase(t *testing.T) {
	fake := &fakeTryTriggerClient{}
	s := sessionAs("bot-1")
	m := triggerMessage("guild-1", "chan-1", "author-1", "<@bot-1> ok")
	m.Mentions = []*discordgo.User{{ID: "bot-1"}}

	forced := mentionsBot(s, m)
	if !forced {
		t.Fatal("test premise broken: mentionsBot did not report a deliberate mention")
	}

	attemptTrigger(s, m, forced, &client.Clients{Trigger: fake})

	if fake.calls != 1 {
		t.Fatalf("TryTrigger was called %d times, want 1", fake.calls)
	}
	if !fake.lastReq.GetForced() {
		t.Error("TryTriggerReq.Forced = false, want true for a message that mentioned the bot")
	}
	if strings.Contains(fake.lastReq.GetPhrase(), "<@bot-1>") {
		t.Errorf("phrase %q still carries the bot's own mention token", fake.lastReq.GetPhrase())
	}
}
