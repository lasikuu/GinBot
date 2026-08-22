package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
)

// sessionAs builds the minimal session the dispatch filter reads: the bot's own
// identity. discordgo.State embeds Ready, which carries the User.
func sessionAs(id string) *discordgo.Session {
	return &discordgo.Session{
		State: &discordgo.State{
			Ready: discordgo.Ready{
				User: &discordgo.User{ID: id},
			},
		},
	}
}

func messageFrom(author *discordgo.User) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{Author: author},
	}
}

// TestIsHuman covers the acceptance criterion that the bot ignores itself and
// other bots. Getting this wrong is what produces a self-reply loop, so the
// degenerate shapes are covered too: discordgo leaves State unpopulated until
// the session is open, and a webhook message has no Author.
func TestIsHuman(t *testing.T) {
	tests := []struct {
		name    string
		session *discordgo.Session
		message *discordgo.MessageCreate
		want    bool
	}{
		{
			name:    "a human is dispatched",
			session: sessionAs("self"),
			message: messageFrom(&discordgo.User{ID: "someone"}),
			want:    true,
		},
		{
			name:    "the bot ignores itself",
			session: sessionAs("self"),
			message: messageFrom(&discordgo.User{ID: "self"}),
			want:    false,
		},
		{
			name:    "the bot ignores itself even when not flagged as a bot",
			session: sessionAs("self"),
			message: messageFrom(&discordgo.User{ID: "self", Bot: false}),
			want:    false,
		},
		{
			name:    "another bot is ignored",
			session: sessionAs("self"),
			message: messageFrom(&discordgo.User{ID: "other", Bot: true}),
			want:    false,
		},
		{
			name:    "a message with no author is ignored",
			session: sessionAs("self"),
			message: messageFrom(nil),
			want:    false,
		},
		{
			name:    "an unpopulated state does not panic",
			session: &discordgo.Session{},
			message: messageFrom(&discordgo.User{ID: "someone"}),
			want:    true,
		},
		{
			name:    "a state with no user does not panic",
			session: &discordgo.Session{State: &discordgo.State{}},
			message: messageFrom(&discordgo.User{ID: "someone"}),
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHuman(tt.session, tt.message); got != tt.want {
				t.Errorf("isHuman() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReRollCommandName covers both the current namespaced IDs and the
// hand-written ones still attached to messages posted before the namespace
// existed. Discord components never expire, so the legacy path stays live
// indefinitely.
func TestReRollCommandName(t *testing.T) {
	tests := []struct {
		name     string
		customID string
		want     string
		wantOK   bool
	}{
		{name: "namespaced", customID: "reroll:doubles", want: "doubles", wantOK: true},
		{name: "namespaced other command", customID: "reroll:sexts", want: "sexts", wantOK: true},
		{name: "legacy doubles", customID: "reRollDoubles", want: "doubles", wantOK: true},
		{name: "legacy triples", customID: "reRollTriples", want: "triples", wantOK: true},
		{name: "unrelated component", customID: "somethingElse", wantOK: false},
		{name: "empty", customID: "", wantOK: false},
		{name: "prefix only yields an empty name", customID: "reroll:", want: "", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := reRollCommandName(tt.customID)
			if ok != tt.wantOK {
				t.Fatalf("reRollCommandName(%q) ok = %v, want %v", tt.customID, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("reRollCommandName(%q) = %q, want %q", tt.customID, got, tt.want)
			}
		})
	}

	// The legacy path is the one nobody exercises by hand, so a legacy ID added
	// without a case above must fail here rather than go unnoticed.
	covered := make(map[string]struct{}, len(legacyReRollIDs))
	for _, tt := range tests {
		if _, legacy := legacyReRollIDs[tt.customID]; legacy {
			covered[tt.customID] = struct{}{}
		}
	}
	for customID := range legacyReRollIDs {
		if _, ok := covered[customID]; !ok {
			t.Errorf("legacy custom ID %q has no case", customID)
		}
	}
}

// TestReRollButtonStaysClickable pins that the button a first roll carries is
// one the component dispatcher resolves back to the same command. Nothing else
// connects the ID stamped onto the button to the ID the dispatcher reads, and
// this is what lets the original message be clicked over and over now that a
// click replies to it instead of replacing it.
func TestReRollButtonStaysClickable(t *testing.T) {
	for _, roll := range digitRolls {
		t.Run(roll.name, func(t *testing.T) {
			resp := &command.Response{Content: "444", ReRollID: reRollID(roll.name)}

			if !hasReRollButton(planResponse(sourceSlash, resp).components, resp.ReRollID) {
				t.Fatalf("a first %s roll carries no button bound to %q", roll.name, resp.ReRollID)
			}

			name, ok := reRollCommandName(resp.ReRollID)
			if !ok || name != roll.name {
				t.Errorf("reRollCommandName(%q) = %q, %v; want %q, true", resp.ReRollID, name, ok, roll.name)
			}
		})
	}
}

// TestLegacyClickProducesNoNewButton covers the legacy path end to end: an old
// hand-written custom ID still resolves, and the roll the click produces
// carries no button. Buttons on already-posted messages never expire, so the
// legacy path must not start a chain either.
func TestLegacyClickProducesNoNewButton(t *testing.T) {
	for customID, want := range legacyReRollIDs {
		t.Run(customID, func(t *testing.T) {
			name, ok := reRollCommandName(customID)
			if !ok || name != want {
				t.Fatalf("reRollCommandName(%q) = %q, %v; want %q, true", customID, name, ok, want)
			}

			resp := &command.Response{Content: "44", ReRollID: reRollID(name)}
			if hasReRollButton(planResponse(sourceReRoll, resp).components, resp.ReRollID) {
				t.Error("a legacy re-roll produced another button, which would chain")
			}
		})
	}
}

// TestReRollIDRoundTrip pins that a generated ID is one the dispatcher can
// resolve. The two live in different files and nothing else connects them.
func TestReRollIDRoundTrip(t *testing.T) {
	for _, roll := range digitRolls {
		t.Run(roll.name, func(t *testing.T) {
			got, ok := reRollCommandName(reRollID(roll.name))
			if !ok || got != roll.name {
				t.Errorf("reRollCommandName(reRollID(%q)) = %q, %v; want %q, true", roll.name, got, ok, roll.name)
			}
		})
	}
}

// TestInvocationFromOptions covers the slash-command binding path. Every
// discordgo option accessor panics when the option is not of its type, and
// discordgo does not recover panics raised in a handler, so an unhandled type
// must return an error rather than reach an accessor.
func TestInvocationFromOptions(t *testing.T) {
	cmd := numberCommand()

	tests := []struct {
		name      string
		options   []*discordgo.ApplicationCommandInteractionDataOption
		wantLower int64
		wantUpper int64
		wantErr   bool
	}{
		{
			name:      "no options fall back to the declared defaults",
			options:   nil,
			wantLower: numberDefaultLower,
			wantUpper: numberDefaultUpper,
		},
		{
			name: "both bounds supplied",
			options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "lower", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(3)},
				{Name: "upper", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(7)},
			},
			wantLower: 3,
			wantUpper: 7,
		},
		{
			// Discord omits an unsupplied option entirely, so this shape is what
			// `/number upper:5` actually delivers. It cannot be expressed
			// positionally, which is why the named binding path exists.
			name: "only the second bound supplied",
			options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "upper", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(5)},
			},
			wantLower: numberDefaultLower,
			wantUpper: 5,
		},
		{
			name: "an unsupported option type is refused, not passed to a panicking accessor",
			options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "lower", Type: discordgo.ApplicationCommandOptionUser, Value: "1234"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := invocationFromOptions(cmd, tt.options)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("invocationFromOptions: %v", err)
			}
			if got := inv.Int("lower"); got != tt.wantLower {
				t.Errorf("lower = %d, want %d", got, tt.wantLower)
			}
			if got := inv.Int("upper"); got != tt.wantUpper {
				t.Errorf("upper = %d, want %d", got, tt.wantUpper)
			}
		})
	}
}

// TestSlashAndChatBindEquivalently is the pure half of "the same command works
// both ways": a slash invocation with no options and a chat invocation with no
// arguments must produce the same bound values.
func TestSlashAndChatBindEquivalently(t *testing.T) {
	cmd := numberCommand()

	slash, err := invocationFromOptions(cmd, nil)
	if err != nil {
		t.Fatalf("slash bind: %v", err)
	}

	chat, err := command.Bind(cmd, nil)
	if err != nil {
		t.Fatalf("chat bind: %v", err)
	}

	for _, arg := range cmd.Args {
		if slash.Int(arg.Name) != chat.Int(arg.Name) {
			t.Errorf("%s: slash = %d, chat = %d", arg.Name, slash.Int(arg.Name), chat.Int(arg.Name))
		}
	}
}
