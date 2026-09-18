package discord

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// Message is populated only on a component interaction, and clickedMessageReference
// depends on it.
func testComponentInteraction(customID string, clickerID string, clickerUsername string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:        "interaction-click-1",
		Token:     "interaction-click-token",
		AppID:     "app-1",
		ChannelID: "chan-1",
		Type:      discordgo.InteractionMessageComponent,
		Data:      discordgo.MessageComponentInteractionData{CustomID: customID},
		Message:   &discordgo.Message{ID: "clicked-msg-1", ChannelID: "chan-1"},
		Member:    &discordgo.Member{User: &discordgo.User{ID: clickerID, Username: clickerUsername}},
	}}
}

// The DM shape: Member is nil and User carries the clicker, which is the branch
// interactionUser exists for.
func testDMComponentInteraction(customID string, clickerID string, clickerUsername string) *discordgo.InteractionCreate {
	i := testComponentInteraction(customID, clickerID, clickerUsername)
	i.Member = nil
	i.User = &discordgo.User{ID: clickerID, Username: clickerUsername}

	return i
}

// findRequest returns the first captured request matching method and a path
// substring, so a test can distinguish the callback, a channel message send
// and a webhook follow-up/edit, which all hit different endpoints.
// setTestRegistry restores the package-level registry, which several tests in
// this package share and overwrite.
func setTestRegistry(t *testing.T) {
	t.Helper()
	previous := commandRegistry
	t.Cleanup(func() { commandRegistry = previous })
	commandRegistry = newTestRegistry(t)
}

func findRequest(requests []capturedRequest, method string, pathContains string) (capturedRequest, bool) {
	for _, r := range requests {
		if r.method == method && strings.Contains(r.path, pathContains) {
			return r, true
		}
	}
	return capturedRequest{}, false
}

// reRollButtonCustomIDs extracts every button custom_id from a captured
// "components" field, which is nested as [{"components":[{"custom_id":...}]}]
// for both an InteractionResponseData and a WebhookEdit body.
func reRollButtonCustomIDs(components any) []string {
	rows, _ := components.([]any)
	var ids []string
	for _, row := range rows {
		rowMap, ok := row.(map[string]any)
		if !ok {
			continue
		}
		nested, _ := rowMap["components"].([]any)
		for _, n := range nested {
			buttonMap, ok := n.(map[string]any)
			if !ok {
				continue
			}
			if id, ok := buttonMap["custom_id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

type erroringEntertainmentClient struct {
	ginbotv1connect.EntertainmentServiceClient
}

func (f *erroringEntertainmentClient) GetRandomNumber(_ context.Context, _ *connect.Request[pb.GetRandomNumberReq]) (*connect.Response[pb.GetRandomNumberResp], error) {
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("dial tcp: connection refused"))
}

// Drives a click through handleInteraction, the platform's real entry point, so
// custom-ID resolution, the registry lookup and the dispatch are all covered.
// Asserting planResponse alone is what let the original defect ship.
func TestButtonClickPostsReRollNamingTheClickerWithNoButtonAndLeavesTheClickedMessageAlone(t *testing.T) {
	setTestRegistry(t)

	transport := &captureTransport{}
	s := testSessionWithTransport(transport)
	i := testComponentInteraction("reroll:doubles", "user-2", "Clicker")

	handleInteraction(s, i, &client.Clients{Entertainment: &fakeEntertainmentClient{}})

	requests := transport.all()

	callback, ok := findRequest(requests, http.MethodPost, "/callback")
	if !ok {
		t.Fatal("no interaction callback was sent; the click was never acknowledged")
	}
	gotType, _ := callback.body["type"].(float64)
	if discordgo.InteractionResponseType(gotType) != discordgo.InteractionResponseDeferredMessageUpdate {
		t.Errorf("callback type = %v, want %d (DeferredMessageUpdate, not DeferredChannelMessageWithSource)",
			callback.body["type"], discordgo.InteractionResponseDeferredMessageUpdate)
	}

	channelPost, ok := findRequest(requests, http.MethodPost, "/channels/"+i.ChannelID+"/messages")
	if !ok {
		t.Fatal("no channel message was posted for the re-roll")
	}
	// fakeEntertainmentClient always rolls "5", which digitRollHandler bolds as
	// an all-digits-match; a mention of the clicker follows the number.
	if content, _ := channelPost.body["content"].(string); content != "**5** <@user-2>" {
		t.Errorf("content = %q, want \"**5** <@user-2>\": the number then a mention of the clicker", content)
	}
	// The attribution is a mention, so it renders as a display name; suppressing
	// mention parsing is the only thing keeping it from pinging on every click.
	requireMentionsSuppressed(t, requests)
	if ids := reRollButtonCustomIDs(channelPost.body["components"]); len(ids) != 0 {
		t.Errorf("re-roll reply carries buttons %v, want none: a re-roll must not chain", ids)
	}
	// An interaction callback carries no message_reference, so the reply is the
	// only thing tying the new roll to the message that was clicked.
	reference, _ := channelPost.body["message_reference"].(map[string]any)
	if got, _ := reference["message_id"].(string); got != i.Message.ID {
		t.Errorf("message_reference.message_id = %q, want %q; the re-roll must reply to the clicked message",
			got, i.Message.ID)
	}

	for _, r := range requests {
		if r.method == http.MethodPatch {
			t.Errorf("unexpected PATCH %s; a component click must not edit the clicked message", r.path)
		}
	}
	if followUp, found := findRequest(requests, http.MethodPost, "/webhooks/"); found {
		t.Errorf("unexpected follow-up %+v on the success path", followUp)
	}

	callbackIndex, channelIndex := -1, -1
	for idx, r := range requests {
		if r.method == http.MethodPost && strings.Contains(r.path, "/callback") {
			callbackIndex = idx
		}
		if r.method == http.MethodPost && strings.Contains(r.path, "/channels/") {
			channelIndex = idx
		}
	}
	if callbackIndex < 0 || channelIndex < 0 || callbackIndex > channelIndex {
		t.Errorf("callback (index %d) did not precede the channel post (index %d); "+
			"the click must be acknowledged before the handler runs", callbackIndex, channelIndex)
	}
}

// Every digit roll shares one re-roll dispatch, and the clicker is resolved from
// whichever of Member.User or User the interaction carries, so the attribution
// must hold for all five commands in both the guild and the DM shape.
func TestEveryDigitRollReRollMentionsTheClicker(t *testing.T) {
	setTestRegistry(t)

	shapes := []struct {
		name  string
		build func(string, string, string) *discordgo.InteractionCreate
	}{
		{name: "guild", build: testComponentInteraction},
		{name: "dm", build: testDMComponentInteraction},
	}

	for _, roll := range digitRolls {
		for _, shape := range shapes {
			t.Run(roll.name+"/"+shape.name, func(t *testing.T) {
				transport := &captureTransport{}
				s := testSessionWithTransport(transport)
				i := shape.build(reRollID(roll.name), "user-9", "Clicker")

				handleInteraction(s, i, &client.Clients{Entertainment: &fakeEntertainmentClient{}})

				requests := transport.all()
				post, ok := findRequest(requests, http.MethodPost, "/channels/"+i.ChannelID+"/messages")
				if !ok {
					t.Fatal("no channel message was posted for the re-roll")
				}
				if content, _ := post.body["content"].(string); content != "**5** <@user-9>" {
					t.Errorf("content = %q, want \"**5** <@user-9>\": a mention, not the username", content)
				}
				requireMentionsSuppressed(t, requests)
			})
		}
	}
}

// After a DeferredMessageUpdate, InteractionResponseEdit lands on the clicked
// message and strips its button, so a failed re-roll reports by follow-up.
func TestButtonClickHandlerErrorSendsEphemeralFollowUpAndNoEdit(t *testing.T) {
	setTestRegistry(t)

	transport := &captureTransport{}
	s := testSessionWithTransport(transport)
	i := testComponentInteraction("reroll:doubles", "user-3", "Unlucky")

	handleInteraction(s, i, &client.Clients{Entertainment: &erroringEntertainmentClient{}})

	requests := transport.all()

	followUp, ok := findRequest(requests, http.MethodPost, "/webhooks/")
	if !ok {
		t.Fatal("no follow-up was sent after the handler failed")
	}
	flags, _ := followUp.body["flags"].(float64)
	if int(flags)&int(discordgo.MessageFlagsEphemeral) == 0 {
		t.Errorf("follow-up flags = %v, want the ephemeral bit set", followUp.body["flags"])
	}

	for _, r := range requests {
		if r.method == http.MethodPatch {
			t.Errorf("unexpected PATCH %s; a failed re-roll must not edit the clicked message", r.path)
		}
	}
	if _, found := findRequest(requests, http.MethodPost, "/channels/"+i.ChannelID+"/messages"); found {
		t.Error("a channel message was sent despite the handler failing")
	}
}

// A slash /doubles is itself a first roll, so it still defers publicly and
// keeps its button through respondDeferred.
func TestSlashDigitRollDefersPubliclyAndKeepsReRollButton(t *testing.T) {
	transport := &captureTransport{}
	s := testSessionWithTransport(transport)
	i := testSlashInteraction()

	cmd := digitRollCommands()[0]
	inv, err := command.Bind(cmd, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	runInteraction(s, i, cmd, inv, &client.Clients{Entertainment: &fakeEntertainmentClient{}})

	requests := transport.all()

	deferReq, ok := findRequest(requests, http.MethodPost, "/callback")
	if !ok {
		t.Fatal("no deferral callback was sent")
	}
	if deferredEphemeral(t, deferReq) {
		t.Error("doubles deferred ephemerally, want public")
	}

	editReq, ok := findRequest(requests, http.MethodPatch, "/webhooks/")
	if !ok {
		t.Fatal("no interaction edit was sent for the slow command's reply")
	}
	wantID := reRollID(cmd.Name)
	if ids := reRollButtonCustomIDs(editReq.body["components"]); len(ids) != 1 || ids[0] != wantID {
		t.Errorf("re-roll buttons on the slash reply = %v, want exactly [%q]", ids, wantID)
	}
}

// A chat-prefix roll has no interaction to defer, but carries the same button.
func TestChatPrefixDigitRollStillCarriesReRollButton(t *testing.T) {
	setTestRegistry(t)

	transport := &captureTransport{}
	s := testSessionWithTransport(transport)
	m := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "msg-1",
		ChannelID: "chan-1",
		GuildID:   "guild-1",
		Author:    &discordgo.User{ID: "user-1", Username: "kohana"},
	}}

	dispatchChatCommand(s, m, "doubles", nil, &client.Clients{Entertainment: &fakeEntertainmentClient{}})

	sendReq, ok := findRequest(transport.all(), http.MethodPost, "/channels/"+m.ChannelID+"/messages")
	if !ok {
		t.Fatal("no channel message was sent for the chat-prefix roll")
	}
	wantID := reRollID("doubles")
	if ids := reRollButtonCustomIDs(sendReq.body["components"]); len(ids) != 1 || ids[0] != wantID {
		t.Errorf("re-roll buttons on the chat-prefix reply = %v, want exactly [%q]", ids, wantID)
	}
}
