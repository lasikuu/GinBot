package discord

import (
	"slices"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
)

// sessionAs builds the minimal session carrying the bot's own identity.
func sessionAs(id string) *discordgo.Session {
	return &discordgo.Session{
		State: &discordgo.State{
			User: &discordgo.User{ID: id},
		},
	}
}

func messageFrom(author *discordgo.User) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{Author: author},
	}
}

// TestIsHuman: the bot ignores itself and other bots. Degenerate shapes are
// covered too, since State is unpopulated until the session opens.
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

// TestReRollCommandName covers namespaced and legacy IDs; Discord components
// never expire, so the legacy path stays live.
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

	// A legacy ID added without a case above must fail here.
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

// TestReRollButtonStaysClickable: the button a first roll carries resolves back
// to the same command; nothing else connects the two IDs.
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

// TestLegacyClickProducesNoNewButton: a legacy custom ID resolves and the roll
// it produces carries no button, so the legacy path starts no chain.
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

// TestReRollIDRoundTrip: a generated ID is one the dispatcher can resolve.
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

// TestInvocationFromOptions: every discordgo option accessor panics on a type
// mismatch, so an unhandled type must return an error, not reach an accessor.
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
			// Discord omits an unsupplied option, so this is what `/number upper:5`
			// delivers — inexpressible positionally.
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

// subCommandInteraction builds the data Discord delivers for /group sub:
// data.Name is the group and the single option is the subcommand.
func subCommandInteraction(group, sub string, args ...*discordgo.ApplicationCommandInteractionDataOption) discordgo.ApplicationCommandInteractionData {
	return discordgo.ApplicationCommandInteractionData{
		Name: group,
		Options: []*discordgo.ApplicationCommandInteractionDataOption{
			{
				Name:    sub,
				Type:    discordgo.ApplicationCommandOptionSubCommand,
				Options: args,
			},
		},
	}
}

// TestResolveApplicationCommandDescendsIntoSubcommands: reading a group's
// data.Options directly would bind the subcommand as the first argument.
func TestResolveApplicationCommandDescendsIntoSubcommands(t *testing.T) {
	commandRegistry = newTestRegistry(t)

	when := &discordgo.ApplicationCommandInteractionDataOption{
		Name:  "when",
		Type:  discordgo.ApplicationCommandOptionString,
		Value: "in 2 hours",
	}
	message := &discordgo.ApplicationCommandInteractionDataOption{
		Name:  "message",
		Type:  discordgo.ApplicationCommandOptionString,
		Value: "tea",
	}

	tests := []struct {
		name        string
		data        discordgo.ApplicationCommandInteractionData
		wantCommand string
		wantOptions []string
	}{
		{
			name:        "grouped subcommand with arguments",
			data:        subCommandInteraction(reminderGroup, reminderSubAdd, when, message),
			wantCommand: "remind",
			wantOptions: []string{"when", "message"},
		},
		{
			name:        "grouped subcommand with no arguments",
			data:        subCommandInteraction(reminderGroup, reminderSubList),
			wantCommand: "reminders",
		},
		{
			name:        "subcommand name in a different case",
			data:        subCommandInteraction(reminderGroup, strings.ToUpper(reminderSubAdd), when, message),
			wantCommand: "remind",
			wantOptions: []string{"when", "message"},
		},
		{
			name: "ungrouped command keeps its own options",
			data: discordgo.ApplicationCommandInteractionData{
				Name: "number",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Name: "lower", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(3)},
				},
			},
			wantCommand: "number",
			wantOptions: []string{"lower"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, options, ok := resolveApplicationCommand(tt.data)
			if !ok {
				t.Fatalf("resolveApplicationCommand(%+v) did not resolve", tt.data)
			}
			if cmd.Name != tt.wantCommand {
				t.Errorf("resolved to %q, want %q", cmd.Name, tt.wantCommand)
			}

			got := make([]string, 0, len(options))
			for _, option := range options {
				got = append(got, option.Name)
			}
			if !slices.Equal(got, tt.wantOptions) {
				t.Errorf("options = %q, want %q", got, tt.wantOptions)
			}

			if _, err := invocationFromOptions(cmd, options); err != nil {
				t.Errorf("invocationFromOptions: %v", err)
			}
		})
	}
}

// TestResolveApplicationCommandRefusesDegenerateShapes: each must report a miss
// so the caller answers the interaction rather than leaving it unanswered.
func TestResolveApplicationCommandRefusesDegenerateShapes(t *testing.T) {
	commandRegistry = newTestRegistry(t)

	tests := []struct {
		name string
		data discordgo.ApplicationCommandInteractionData
	}{
		{
			name: "unknown command",
			data: discordgo.ApplicationCommandInteractionData{Name: "nosuchcommand"},
		},
		{
			// A stale registration could send a group with no subcommand.
			name: "group with no options",
			data: discordgo.ApplicationCommandInteractionData{Name: reminderGroup},
		},
		{
			name: "group whose first option is not a subcommand",
			data: discordgo.ApplicationCommandInteractionData{
				Name: reminderGroup,
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Name: "when", Type: discordgo.ApplicationCommandOptionString, Value: "in 2h"},
				},
			},
		},
		{
			name: "group with an unknown subcommand",
			data: subCommandInteraction(reminderGroup, "nosuchsub"),
		},
		{
			// A member's flat name is not its sub, so it must not resolve here.
			name: "group followed by a member's flat name",
			data: subCommandInteraction(reminderGroup, "remind"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, options, ok := resolveApplicationCommand(tt.data)
			if ok {
				t.Fatalf("resolveApplicationCommand(%+v) resolved to %q, want a miss", tt.data, cmd.Name)
			}
			if cmd.Name != "" {
				t.Errorf("Name = %q, want the zero Command on a miss", cmd.Name)
			}
			if len(options) != 0 {
				t.Errorf("options = %v, want none on a miss", options)
			}
		})
	}
}

func TestIsCommandGroup(t *testing.T) {
	commandRegistry = newTestRegistry(t)

	tests := []struct {
		name string
		want bool
	}{
		{name: reminderGroup, want: true},
		{name: strings.ToUpper(reminderGroup), want: true},
		{name: "remind", want: false},
		{name: "ping", want: false},
		{name: "", want: false},
		{name: "nosuchgroup", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCommandGroup(tt.name); got != tt.want {
				t.Errorf("isCommandGroup(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestSlashAndChatBindEquivalently: a slash and a chat invocation with no
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

// TestMessageContentRequired pins when the privileged MESSAGE_CONTENT intent is
// asked for: only when chat commands or triggers need it.
func TestMessageContentRequired(t *testing.T) {
	tests := []struct {
		name           string
		prefixes       []string
		messageContent bool
		want           bool
	}{
		{name: "neither chat commands nor triggers", prefixes: nil, messageContent: false, want: false},
		{name: "an empty prefix list is not a prefix", prefixes: []string{}, messageContent: false, want: false},
		{name: "chat commands alone", prefixes: []string{"??"}, messageContent: false, want: true},
		{name: "triggers alone", prefixes: nil, messageContent: true, want: true},
		{name: "both", prefixes: []string{"??"}, messageContent: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageContentRequired(tt.prefixes, tt.messageContent); got != tt.want {
				t.Errorf("messageContentRequired(%q, %v) = %v, want %v",
					tt.prefixes, tt.messageContent, got, tt.want)
			}
		})
	}
}

// TestTriggerCandidate: a prefixed message is a command and never also a trigger,
// whether or not the command resolves; blank content is disqualified.
func TestTriggerCandidate(t *testing.T) {
	prefixes := []string{"?", "??"}

	tests := []struct {
		name     string
		content  string
		prefixes []string
		want     bool
	}{
		{name: "ordinary conversation", content: "good morning everyone", prefixes: prefixes, want: true},
		{name: "a resolvable command", content: "??ping", prefixes: prefixes, want: false},
		{name: "a command that does not resolve", content: "??nosuchcommand", prefixes: prefixes, want: false},
		{name: "the shorter prefix also disqualifies", content: "?ping", prefixes: prefixes, want: false},
		{name: "a bare prefix", content: "??", prefixes: prefixes, want: false},
		{name: "empty", content: "", prefixes: prefixes, want: false},
		{name: "spaces only", content: "   ", prefixes: prefixes, want: false},
		{name: "newlines and tabs only", content: "\n\t \n", prefixes: prefixes, want: false},
		{
			// With no prefixes configured, a prefix-looking message is conversation.
			name:     "a prefix that is not configured is not a prefix",
			content:  "??ping",
			prefixes: nil,
			want:     true,
		},
		{name: "conversation with no prefixes configured", content: "good morning", prefixes: nil, want: true},
		{
			name:     "a prefix in the middle",
			content:  "what does ??ping do",
			prefixes: prefixes,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := triggerCandidate(tt.content, tt.prefixes); got != tt.want {
				t.Errorf("triggerCandidate(%q, %q) = %v, want %v", tt.content, tt.prefixes, got, tt.want)
			}
		})
	}
}

// messageMentioning builds a deliberate mention: users in Mentions and their
// <@id> tokens in the content.
func messageMentioning(users ...*discordgo.User) *discordgo.MessageCreate {
	m := messageFrom(&discordgo.User{ID: "someone"})
	m.Mentions = users

	content := "hello"
	for _, user := range users {
		if user != nil {
			content += " <@" + user.ID + ">"
		}
	}
	m.Content = content

	return m
}

// messageReplyPinging builds a reply ping: the author is in Mentions but no
// mention token appears in the text.
func messageReplyPinging(users ...*discordgo.User) *discordgo.MessageCreate {
	m := messageFrom(&discordgo.User{ID: "someone"})
	m.Mentions = users
	m.Content = "sure, thanks"

	return m
}

// withContent overrides a message's text so the token and the Mentions list can
// disagree.
func withContent(m *discordgo.MessageCreate, content string) *discordgo.MessageCreate {
	m.Content = content

	return m
}

// TestMentionsBot: a deliberate mention is decided by the bot's user id and by
// the message text, not the Mentions list alone (which a reply ping populates).
func TestMentionsBot(t *testing.T) {
	tests := []struct {
		name    string
		session *discordgo.Session
		message *discordgo.MessageCreate
		want    bool
	}{
		{
			name:    "the bot is mentioned",
			session: sessionAs("self"),
			message: messageMentioning(&discordgo.User{ID: "self"}),
			want:    true,
		},
		{
			name:    "somebody else is mentioned",
			session: sessionAs("self"),
			message: messageMentioning(&discordgo.User{ID: "someone-else"}),
			want:    false,
		},
		{
			name:    "the bot is mentioned alongside others",
			session: sessionAs("self"),
			message: messageMentioning(&discordgo.User{ID: "someone-else"}, &discordgo.User{ID: "self"}),
			want:    true,
		},
		{
			name:    "no mentions at all",
			session: sessionAs("self"),
			message: messageMentioning(),
			want:    false,
		},
		{
			name:    "a reply ping is not a deliberate mention",
			session: sessionAs("self"),
			message: messageReplyPinging(&discordgo.User{ID: "self"}),
			want:    false,
		},
		{
			name:    "a nickname mention token counts",
			session: sessionAs("self"),
			message: withContent(messageReplyPinging(&discordgo.User{ID: "self"}), "oi <@!self> speak"),
			want:    true,
		},
		{
			// The token alone must not be enough, or a member could forge one.
			name:    "a bare token with nothing resolved does not count",
			session: sessionAs("self"),
			message: withContent(messageReplyPinging(), "<@self>"),
			want:    false,
		},
		{
			name:    "a nil entry in the mention list does not panic",
			session: sessionAs("self"),
			message: messageMentioning(nil, &discordgo.User{ID: "self"}),
			want:    true,
		},
		{
			name:    "another user's mention token does not count",
			session: sessionAs("self"),
			message: messageMentioning(&discordgo.User{ID: "self-other"}),
			want:    false,
		},
		{
			name:    "only a nil entry",
			session: sessionAs("self"),
			message: messageMentioning(nil),
			want:    false,
		},
		{
			name:    "an unpopulated state does not panic",
			session: &discordgo.Session{},
			message: messageMentioning(&discordgo.User{ID: "self"}),
			want:    false,
		},
		{
			name:    "a state with no user does not panic",
			session: &discordgo.Session{State: &discordgo.State{}},
			message: messageMentioning(&discordgo.User{ID: "self"}),
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mentionsBot(tt.session, tt.message); got != tt.want {
				t.Errorf("mentionsBot() = %v, want %v", got, tt.want)
			}
		})
	}
}
