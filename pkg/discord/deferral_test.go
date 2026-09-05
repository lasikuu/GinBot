package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// ListTriggers can outlast the 3s interaction deadline; unacknowledged, Discord
// answers "Unknown Integration".
func TestTriggerListCommandDefersForTheListRPC(t *testing.T) {
	if !triggerListCommand().Slow {
		t.Error("triggerListCommand().Slow = false, want true")
	}
}

// noRPCCommands is default-closed: a command absent from it must declare Slow.
var noRPCCommands = map[string]bool{
	// help answers from the local command.Registry; nothing crosses the wire.
	"help": true,
	// info reports local process state (build info, uptime, registry size).
	"info": true,
}

// Walks the registry rather than hand-listing, so a new RPC-backed command
// cannot be added without deferral.
func TestEveryRPCBackedCommandDefersWithinDiscordsDeadline(t *testing.T) {
	for _, cmd := range commandDefinitions() {
		if noRPCCommands[cmd.Name] {
			continue
		}
		if !cmd.Slow {
			t.Errorf("%s: Slow = false, want true; its handler calls a Connect RPC, "+
				"which can outlast Discord's 3s interaction deadline", cmd.Name)
		}
	}
}

// TestNoRPCAllowListCommandsReallyMakeNoRPC guards the allow-list itself: an
// entry that in fact calls an RPC would silently exempt a command that needs
// Slow: true just as much as any other.
func TestNoRPCAllowListCommandsReallyMakeNoRPC(t *testing.T) {
	registry := newTestRegistry(t)
	// help and info both read the package-level registry directly, not the
	// caller's own copy.
	commandRegistry = registry

	for name := range noRPCCommands {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("allow-listed command %q is not registered", name)
		}

		// A bare context: clientsFrom(ctx) panics with nil clients, so any call
		// that reaches an RPC fails loudly rather than silently passing.
		ctx := withOrigin(context.Background(), "guild-1", "channel-1")
		if _, err := cmd.Handler(ctx, &command.Invocation{Args: map[string]any{}}); err != nil {
			// help("nonexistent command") is a legitimate InvalidArgument, not an RPC failure.
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("%s: handler failed with a bare context: %v; "+
					"if that failure is an RPC call reaching a nil client, it belongs off this allow-list", name, err)
			}
		}
	}
}

func TestDigitRollAndNumberAreSlowButNotEphemeral(t *testing.T) {
	for _, cmd := range digitRollCommands() {
		if !cmd.Slow {
			t.Errorf("%s: Slow = false, want true (calls GetRandomNumber)", cmd.Name)
		}
		if cmd.Ephemeral {
			t.Errorf("%s: Ephemeral = true, want false; a public roll must stay public", cmd.Name)
		}
	}

	number := numberCommand()
	if !number.Slow {
		t.Error("numberCommand().Slow = false, want true (calls GetRandomNumber)")
	}
	if number.Ephemeral {
		t.Error("numberCommand().Ephemeral = true, want false; a public roll must stay public")
	}
}

// TestTriggerExecStaysPublic: the one trigger command whose reply the channel
// must see, unlike the rest of the group.
func TestTriggerExecStaysPublic(t *testing.T) {
	if triggerExecCommand().Ephemeral {
		t.Error("triggerExecCommand().Ephemeral = true, want false")
	}
}

type fakeEntertainmentClient struct {
	ginbotv1connect.EntertainmentServiceClient
}

func (f *fakeEntertainmentClient) GetRandomNumber(_ context.Context, _ *connect.Request[pb.GetRandomNumberReq]) (*connect.Response[pb.GetRandomNumberResp], error) {
	number := "5"
	return connect.NewResponse(pb.GetRandomNumberResp_builder{Number: &number}.Build()), nil
}

// Deferral must not drop the re-roll button.
func TestDigitRollsStillCarryTheReRollControl(t *testing.T) {
	ctx := guildContext(&client.Clients{Entertainment: &fakeEntertainmentClient{}})

	for _, cmd := range digitRollCommands() {
		t.Run(cmd.Name, func(t *testing.T) {
			resp, err := invokeNamed(t, cmd, ctx, map[string]any{})
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if resp.ReRollID == "" {
				t.Error("ReRollID is empty; the re-roll button was dropped")
			}
		})
	}
}

// A re-roll re-binds with default arguments (see interactions.go), so a command
// taking arguments must not offer one: it would answer a question nobody asked.
func TestCommandsWithArgumentsOfferNoReRoll(t *testing.T) {
	ctx := guildContext(&client.Clients{Entertainment: &fakeEntertainmentClient{}})

	resp, err := invokeNamed(t, numberCommand(), ctx, map[string]any{
		"lower": int64(100),
		"upper": int64(200),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if resp.ReRollID != "" {
		t.Errorf("ReRollID = %q; a re-roll would discard the caller's bounds", resp.ReRollID)
	}
}

// capturedRequest is one HTTP call discordgo made, decoded far enough to
// inspect the interaction response flags.
type capturedRequest struct {
	method string
	path   string
	body   map[string]any
}

// routedResponse is what captureTransport.respond returns for one request.
type routedResponse struct {
	status int
	body   string
}

// captureTransport stands in for the network: discordgo's REST calls go through
// *http.Client.
type captureTransport struct {
	mu       sync.Mutex
	requests []capturedRequest

	// respond, when set, decides the status and body per request; the default
	// is an unconditional 200 with an empty JSON object, which every
	// discordgo response type unmarshals from without error.
	respond func(capturedRequest) routedResponse
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &body)
	}

	captured := capturedRequest{method: req.Method, path: req.URL.Path, body: body}

	c.mu.Lock()
	c.requests = append(c.requests, captured)
	c.mu.Unlock()

	routed := routedResponse{status: http.StatusOK, body: "{}"}
	if c.respond != nil {
		routed = c.respond(captured)
	}

	return &http.Response{
		StatusCode: routed.status,
		Body:       io.NopCloser(strings.NewReader(routed.body)),
		Header:     make(http.Header),
	}, nil
}

func (c *captureTransport) firstOfMethod(method string) (capturedRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, r := range c.requests {
		if r.method == method {
			return r, true
		}
	}
	return capturedRequest{}, false
}

// all returns a snapshot safe to range over after the calls under test finish.
func (c *captureTransport) all() []capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]capturedRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

func testSessionWithTransport(rt http.RoundTripper) *discordgo.Session {
	return &discordgo.Session{
		State:       &discordgo.State{User: &discordgo.User{ID: "bot-1"}},
		Ratelimiter: discordgo.NewRatelimiter(),
		Client:      &http.Client{Transport: rt},
		Token:       "test-token",
	}
}

func testSlashInteraction() *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:        "interaction-1",
		Token:     "interaction-token",
		AppID:     "app-1",
		ChannelID: "chan-1",
		Type:      discordgo.InteractionApplicationCommand,
		User:      &discordgo.User{ID: "user-1", Username: "kohana"},
	}}
}

// deferredFlags reports whether the given deferral request asked for an
// ephemeral acknowledgement, straight from the JSON body discordgo sent.
func deferredEphemeral(t *testing.T, req capturedRequest) bool {
	t.Helper()

	data, _ := req.body["data"].(map[string]any)
	flags, ok := data["flags"]
	if !ok {
		return false
	}

	value, ok := flags.(float64)
	if !ok {
		t.Fatalf("flags is not a number: %#v", flags)
	}

	return int(value)&int(discordgo.MessageFlagsEphemeral) != 0
}

// TestDeferralVisibilityMatchesCommandEphemeral is the core fix: deferring no
// longer forces ephemeral. A public Slow command must be deferred publicly,
// and only an Ephemeral one is deferred privately.
func TestDeferralVisibilityMatchesCommandEphemeral(t *testing.T) {
	tests := []struct {
		name      string
		ephemeral bool
	}{
		{name: "a public slow command defers publicly", ephemeral: false},
		{name: "an ephemeral slow command defers ephemerally", ephemeral: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &captureTransport{}
			s := testSessionWithTransport(transport)
			i := testSlashInteraction()

			cmd := command.Command{
				Name:      "slow-test",
				Slow:      true,
				Ephemeral: tt.ephemeral,
				Handler: func(_ context.Context, _ *command.Invocation) (*command.Response, error) {
					return &command.Response{Content: "done"}, nil
				},
			}
			inv, err := command.Bind(cmd, nil)
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}

			runInteraction(s, i, cmd, inv, &client.Clients{})

			deferReq, ok := transport.firstOfMethod(http.MethodPost)
			if !ok {
				t.Fatal("no deferral request was sent")
			}

			if got := deferredEphemeral(t, deferReq); got != tt.ephemeral {
				t.Errorf("deferral ephemeral flag = %v, want %v (Command.Ephemeral = %v)",
					got, tt.ephemeral, tt.ephemeral)
			}
		})
	}
}

// A file reply carries no text (triggerPlaybackResponse returns an empty
// Content on purpose), and splitContent returns no chunks for it. discordgo
// does not recover a panic in a handler, so a bad slice bound here takes the
// process down rather than failing one interaction.
func TestDeferredFileReplyWithNoContentSendsOneEditAndNoFollowUps(t *testing.T) {
	transport := &captureTransport{}
	s := testSessionWithTransport(transport)
	i := testSlashInteraction()

	cmd := command.Command{
		Name: "file-test",
		Slow: true,
		Handler: func(_ context.Context, _ *command.Invocation) (*command.Response, error) {
			return &command.Response{
				File: &command.ResponseFile{Name: "cat.png", MIMEType: "image/png", Content: []byte{1, 2, 3}},
			}, nil
		},
	}
	inv, err := command.Bind(cmd, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	runInteraction(s, i, cmd, inv, &client.Clients{})

	edits, followUps := 0, 0
	for _, r := range transport.all() {
		switch {
		case r.method == http.MethodPatch:
			edits++
		case r.method == http.MethodPost && strings.Contains(r.path, "/webhooks/"):
			followUps++
		}
	}

	if edits != 1 {
		t.Errorf("interaction edits = %d, want 1", edits)
	}
	if followUps != 0 {
		t.Errorf("follow-up messages = %d, want 0: there is no second chunk", followUps)
	}
}
