package discord

import (
	"slices"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
)

// sessionAs builds the minimal session the dispatch filter reads: the bot's own
// identity. discordgo.State embeds Ready, which carries the User.
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

// subCommandInteraction builds the interaction data Discord actually delivers
// for /group sub: data.Name is the GROUP, and the single option is the
// subcommand, whose own options carry the arguments.
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

// TestResolveApplicationCommandDescendsIntoSubcommands: reading data.Options
// directly for a group would bind the SUBCOMMAND as the first argument and lose
// every real one, so the descent is what makes /reminder add work at all.
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
			// Discord lowercases what it sends, but resolution folds anyway and
			// asserting it here keeps the slash path aligned with the chat one.
			name:        "subcommand name in a different case",
			data:        subCommandInteraction(reminderGroup, strings.ToUpper(reminderSubAdd), when, message),
			wantCommand: "remind",
			wantOptions: []string{"when", "message"},
		},
		{
			// A top-level command is unchanged: its own options are the
			// arguments, with no level to descend.
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

			// The returned options must bind against the resolved command, which
			// is the whole point of returning them together.
			if _, err := invocationFromOptions(cmd, options); err != nil {
				t.Errorf("invocationFromOptions: %v", err)
			}
		})
	}
}

// TestResolveApplicationCommandRefusesDegenerateShapes: every one of these must
// report a miss so the caller answers the interaction. Returning silently is what
// shows the user "the application did not respond", with nothing in the log to
// explain it.
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
			// A group is not invocable on its own; Discord should never send
			// this, but a stale registration could.
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
			// The member's flat name is not its sub, so it must not resolve as
			// one from the slash path either.
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
		// A member's flat name is a command, not a group.
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

// TestMessageContentRequired pins when the privileged intent is asked for.
//
// Requesting MESSAGE_CONTENT when the application does not have it enabled makes
// the gateway close with 4014, which surfaces as a fatal "cannot open the
// session" — so it must not be requested for a bot that needs neither chat
// commands nor triggers. NOT requesting it when either is wanted is the silent
// failure: every MessageCreate arrives with an empty Content and nothing ever
// matches, with no error anywhere.
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

// TestTriggerCandidate is the acceptance criterion that a message which parses
// as a command does NOT also attempt a trigger.
//
// A prefix is an explicit address to a bot, so it disqualifies the message
// whether or not the command behind it resolves: "??nosuchthing" is a typo aimed
// at a bot, not conversation, and matching a trigger on it would answer a typo
// with an auto-responder. Blank content is disqualified because every message now
// costs an RPC and there is nothing to match.
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
			// With no prefixes configured chat commands are disabled entirely,
			// so nothing is an address to this bot and a message that looks like
			// one is still just conversation.
			name:     "a prefix that is not configured is not a prefix",
			content:  "??ping",
			prefixes: nil,
			want:     true,
		},
		{name: "conversation with no prefixes configured", content: "good morning", prefixes: nil, want: true},
		{
			// The prefix has to be at the start. A message merely containing one
			// is conversation.
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

// messageMentioning builds a message that mentions users the way Discord
// delivers a DELIBERATE mention: the users appear in Mentions and their <@id>
// tokens appear in the content.
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

// messageReplyPinging builds a message that mentions users the way Discord
// delivers a REPLY: the replied-to author is added to Mentions, but no mention
// token appears in the text the author actually typed.
func messageReplyPinging(users ...*discordgo.User) *discordgo.MessageCreate {
	m := messageFrom(&discordgo.User{ID: "someone"})
	m.Mentions = users
	m.Content = "sure, thanks"

	return m
}

// withContent overrides a message's text, for the cases where the token in the
// content and the resolved Mentions list have to disagree.
func withContent(m *discordgo.MessageCreate, content string) *discordgo.MessageCreate {
	m.Content = content

	return m
}

// TestMentionsBot covers the forced fire's trigger condition. It must be decided
// against the bot's own user id rather than by string-matching a name: a nickname
// is per-guild and renameable, and "gin" appears in ordinary conversation.
//
// It must also be decided against the message TEXT, not the Mentions list alone.
// Discord adds the replied-to author to Mentions whenever mention_author is set,
// which is the client default, so a list-only check would turn every reply to a
// bot message into a forced fire — bypassing the chance roll on ordinary
// conversation with the bot.
//
// The degenerate shapes are real. discordgo leaves State unpopulated until the
// session is open, Mentions is nil on the overwhelming majority of messages, and
// handlers are dispatched without a recover() — so a nil deref here takes the
// whole process down on somebody's greeting.
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
			// The whole reason the content is consulted.
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
			// Mentions is what Discord resolved; the token alone must not be
			// enough, or a member could forge one for an id the bot never had.
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
