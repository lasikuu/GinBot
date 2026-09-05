package discord

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// handleTriggerUndo's consuming path is not tested here: it deletes two
// messages through a live discordgo.Session, which has no fake.

func undoMessage(channelID, authorID, content string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "undo-message",
			ChannelID: channelID,
			Content:   content,
			Author:    &discordgo.User{ID: authorID},
		},
	}
}

func withAttachment(m *discordgo.MessageCreate) *discordgo.MessageCreate {
	m.Attachments = []*discordgo.MessageAttachment{{ID: "attachment-1"}}

	return m
}

func withSticker(m *discordgo.MessageCreate) *discordgo.MessageCreate {
	m.StickerItems = []*discordgo.StickerItem{{ID: "sticker-1"}}

	return m
}

// Discord sends partial payloads for some events, so an authorless message is a
// shape the handler receives rather than a synthetic one.
func authorlessMessage(channelID, content string) *discordgo.MessageCreate {
	m := messageFrom(nil)
	m.ChannelID = channelID
	m.Content = content

	return m
}

// pendingCount takes the lock, so it is safe beside a concurrent arm or consume.
func pendingCount(w *undoWindows) int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return len(w.pending)
}

// isolateTriggerUndoWindows swaps the package singleton, so a test cannot leave
// an armed window behind for the next one.
func isolateTriggerUndoWindows(t *testing.T) *undoWindows {
	t.Helper()

	previous := triggerUndoWindows
	triggerUndoWindows = newUndoWindows()
	t.Cleanup(func() { triggerUndoWindows = previous })

	return triggerUndoWindows
}

// TestTriggerUndoWindowIsSevenSeconds: the window is how long a fired trigger
// stays cancellable, so widening it silently changes what a later "no" undoes.
func TestTriggerUndoWindowIsSevenSeconds(t *testing.T) {
	if triggerUndoWindow != 7*time.Second {
		t.Errorf("triggerUndoWindow = %v, want %v", triggerUndoWindow, 7*time.Second)
	}
}

// TestIsTriggerUndoPhrase: the phrase is matched whole, so ordinary
// conversation starting with one does not delete the bot's last response.
func TestIsTriggerUndoPhrase(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "no", content: "no", want: true},
		{name: "ei", content: "ei", want: true},
		{name: "del", content: "del", want: true},
		{name: "upper case", content: "NO", want: true},
		{name: "mixed case", content: "Ei", want: true},
		{name: "upper case del", content: "DEL", want: true},
		{name: "surrounding spaces", content: "  NO  ", want: true},
		{name: "surrounding newlines and tabs", content: "\n\tei\n", want: true},
		{name: "empty", content: "", want: false},
		{name: "whitespace only", content: "   ", want: false},
		{name: "a superstring of no", content: "no idea", want: false},
		{name: "a word starting with no", content: "nope", want: false},
		{name: "a word starting with del", content: "deleted", want: false},
		{name: "a word starting with ei", content: "eiköhän", want: false},
		{name: "a phrase in the middle of a sentence", content: "well no, keep it", want: false},
		{name: "a phrase at the end of a sentence", content: "I said del", want: false},
		{name: "two phrases", content: "no ei", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTriggerUndoPhrase(tt.content); got != tt.want {
				t.Errorf("isTriggerUndoPhrase(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}

	// A phrase added without a case above must fail here.
	for phrase := range triggerUndoPhrases {
		if !isTriggerUndoPhrase(strings.ToUpper(phrase)) {
			t.Errorf("declared phrase %q does not match case-insensitively", phrase)
		}
	}
}

func TestUndoWindowIsConsumedOnce(t *testing.T) {
	windows := newUndoWindows()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	key := undoKey{channelID: "channel-1", authorID: "author-1"}

	windows.arm(key, "response-1", now)

	id, ok := windows.consume(key, now)
	if !ok {
		t.Fatal("the armed window could not be consumed")
	}
	if id != "response-1" {
		t.Errorf("consume returned %q, want %q", id, "response-1")
	}

	if id, ok := windows.consume(key, now); ok {
		t.Errorf("a second consume returned %q, want nothing", id)
	}
	if got := pendingCount(windows); got != 0 {
		t.Errorf("pending = %d after the window was taken, want 0", got)
	}
}

// TestUndoWindowIsKeyedByChannelAndAuthor: a bystander, or the same author
// elsewhere, must not delete somebody else's response.
func TestUndoWindowIsKeyedByChannelAndAuthor(t *testing.T) {
	armed := undoKey{channelID: "channel-1", authorID: "author-1"}

	tests := []struct {
		name string
		key  undoKey
	}{
		{name: "another author in the same channel", key: undoKey{channelID: "channel-1", authorID: "author-2"}},
		{name: "the same author in another channel", key: undoKey{channelID: "channel-2", authorID: "author-1"}},
		{name: "neither matches", key: undoKey{channelID: "channel-2", authorID: "author-2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windows := newUndoWindows()
			now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			windows.arm(armed, "response-1", now)

			if id, ok := windows.consume(tt.key, now); ok {
				t.Errorf("consume(%+v) returned %q, want nothing", tt.key, id)
			}
			if _, ok := windows.consume(armed, now); !ok {
				t.Error("the armed window was taken by a key that does not match it")
			}
		})
	}
}

// TestUndoWindowExpiresAtTheDeadline: the entry is dead once now is no longer
// before expiresAt, so the deadline itself is already too late.
func TestUndoWindowExpiresAtTheDeadline(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	deadline := base.Add(triggerUndoWindow)

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "the instant it is armed", now: base, want: true},
		{name: "one nanosecond before the deadline", now: deadline.Add(-time.Nanosecond), want: true},
		{name: "exactly at the deadline", now: deadline, want: false},
		{name: "one nanosecond after the deadline", now: deadline.Add(time.Nanosecond), want: false},
		{name: "long after the deadline", now: deadline.Add(time.Hour), want: false},
	}

	key := undoKey{channelID: "channel-1", authorID: "author-1"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windows := newUndoWindows()
			windows.arm(key, "response-1", base)

			if _, ok := windows.consume(key, tt.now); ok != tt.want {
				t.Errorf("consume at %v ok = %v, want %v", tt.now.Sub(base), ok, tt.want)
			}
		})
	}
}

// TestArmReplacesTheOpenWindow: an undo means the latest response, and the
// replacement carries its own deadline rather than inheriting the first one.
func TestArmReplacesTheOpenWindow(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	later := base.Add(time.Second)
	key := undoKey{channelID: "channel-1", authorID: "author-1"}

	windows := newUndoWindows()
	windows.arm(key, "response-1", base)
	windows.arm(key, "response-2", later)

	if got := pendingCount(windows); got != 1 {
		t.Fatalf("pending = %d after arming the same key twice, want 1", got)
	}

	id, ok := windows.consume(key, later)
	if !ok {
		t.Fatal("the re-armed window could not be consumed")
	}
	if id != "response-2" {
		t.Errorf("consume returned %q, want the response armed last", id)
	}

	fresh := newUndoWindows()
	fresh.arm(key, "response-1", base)
	fresh.arm(key, "response-2", later)

	if _, ok := fresh.consume(key, base.Add(triggerUndoWindow)); !ok {
		t.Error("the re-armed window expired on the first arm's deadline")
	}
}

// TestUndoWindowsPruneOnEveryOperation: without this, a window nobody undoes
// stays in the map for the life of the process.
func TestUndoWindowsPruneOnEveryOperation(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	afterDeadline := base.Add(triggerUndoWindow)

	armThree := func(t *testing.T) *undoWindows {
		t.Helper()

		windows := newUndoWindows()
		for _, author := range []string{"author-1", "author-2", "author-3"} {
			windows.arm(undoKey{channelID: "channel-1", authorID: author}, "response-"+author, base)
		}
		if got := pendingCount(windows); got != 3 {
			t.Fatalf("pending = %d after arming three keys, want 3", got)
		}

		return windows
	}

	t.Run("consume", func(t *testing.T) {
		windows := armThree(t)

		if id, ok := windows.consume(undoKey{channelID: "channel-1", authorID: "nobody"}, afterDeadline); ok {
			t.Errorf("consume returned %q for a key that was never armed", id)
		}
		if got := pendingCount(windows); got != 0 {
			t.Errorf("pending = %d past the deadline, want 0", got)
		}
	})

	t.Run("arm", func(t *testing.T) {
		windows := armThree(t)

		windows.arm(undoKey{channelID: "channel-1", authorID: "author-4"}, "response-4", afterDeadline)
		if got := pendingCount(windows); got != 1 {
			t.Errorf("pending = %d, want only the newly armed window", got)
		}
	})
}

// TestUndoWindowIsConsumedOnceUnderConcurrency: discordgo dispatches every
// MessageCreate on its own goroutine, so two undos can race for one window.
func TestUndoWindowIsConsumedOnceUnderConcurrency(t *testing.T) {
	windows := newUndoWindows()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	key := undoKey{channelID: "channel-1", authorID: "author-1"}

	windows.arm(key, "response-1", now)

	const consumers = 32

	var wg sync.WaitGroup
	var winners atomic.Int64

	for range consumers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			id, ok := windows.consume(key, now)
			if !ok {
				return
			}
			if id != "response-1" {
				t.Errorf("consume returned %q, want %q", id, "response-1")
			}
			winners.Add(1)
		}()
	}
	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Errorf("%d of %d consumers took the same window, want exactly 1", got, consumers)
	}
}

func TestUndoWindowsSurviveConcurrentArmAndConsume(t *testing.T) {
	windows := newUndoWindows()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	const goroutines = 64

	var wg sync.WaitGroup

	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			author := strconv.Itoa(i)
			key := undoKey{channelID: "channel-1", authorID: author}
			want := "response-" + author

			windows.arm(key, want, now)

			id, ok := windows.consume(key, now)
			if !ok {
				t.Errorf("author %s could not consume the window it armed", author)
				return
			}
			if id != want {
				t.Errorf("author %s consumed %q, want %q", author, id, want)
			}
		}()
	}
	wg.Wait()

	if got := pendingCount(windows); got != 0 {
		t.Errorf("pending = %d, want 0; every armed window was consumed", got)
	}
}

// TestArmTriggerUndoOpensTheWindowForTheAuthorAndChannel is the wiring
// attemptTrigger relies on: the id armed is the response a later undo deletes.
func TestArmTriggerUndoOpensTheWindowForTheAuthorAndChannel(t *testing.T) {
	windows := isolateTriggerUndoWindows(t)

	armTriggerUndo(undoMessage("channel-1", "author-1", "kissa"), &discordgo.Message{ID: "response-1"})

	if id, ok := windows.consume(undoKey{channelID: "channel-1", authorID: "author-2"}, time.Now()); ok {
		t.Errorf("another author consumed the window and got %q", id)
	}

	id, ok := windows.consume(undoKey{channelID: "channel-1", authorID: "author-1"}, time.Now())
	if !ok {
		t.Fatal("no window was armed for the author whose message fired the trigger")
	}
	if id != "response-1" {
		t.Errorf("armed response id = %q, want %q", id, "response-1")
	}
}

// TestArmTriggerUndoIgnoresWhatCannotBeUndone: a nil sent message means the
// send failed, so an armed window would delete an unrelated later message.
func TestArmTriggerUndoIgnoresWhatCannotBeUndone(t *testing.T) {
	tests := []struct {
		name    string
		message *discordgo.MessageCreate
		sent    *discordgo.Message
	}{
		{
			name:    "nil message",
			message: nil,
			sent:    &discordgo.Message{ID: "response-1"},
		},
		{
			name:    "a message with no author",
			message: authorlessMessage("channel-1", "kissa"),
			sent:    &discordgo.Message{ID: "response-1"},
		},
		{
			name:    "the response was never sent",
			message: undoMessage("channel-1", "author-1", "kissa"),
			sent:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windows := isolateTriggerUndoWindows(t)

			armTriggerUndo(tt.message, tt.sent)

			if got := pendingCount(windows); got != 0 {
				t.Errorf("pending = %d, want 0; nothing here can be undone", got)
			}
		})
	}
}

// TestHandleTriggerUndoLeavesTheSessionAloneOnEveryEarlyReturn: the nil session
// is the seam — a branch that reaches Discord panics instead of passing.
func TestHandleTriggerUndoLeavesTheSessionAloneOnEveryEarlyReturn(t *testing.T) {
	tests := []struct {
		name    string
		armKey  undoKey
		expired bool
		message *discordgo.MessageCreate
	}{
		{
			name:    "nil message",
			armKey:  undoKey{channelID: "channel-1", authorID: "author-1"},
			message: nil,
		},
		{
			name:    "a message with no author",
			armKey:  undoKey{channelID: "channel-1", authorID: ""},
			message: authorlessMessage("channel-1", "no"),
		},
		{
			name:    "content that only contains a phrase",
			armKey:  undoKey{channelID: "channel-1", authorID: "author-1"},
			message: undoMessage("channel-1", "author-1", "no idea"),
		},
		{
			name:    "ordinary conversation",
			armKey:  undoKey{channelID: "channel-1", authorID: "author-1"},
			message: undoMessage("channel-1", "author-1", "kissa"),
		},
		{
			name:    "a phrase captioning an attachment",
			armKey:  undoKey{channelID: "channel-1", authorID: "author-1"},
			message: withAttachment(undoMessage("channel-1", "author-1", "no")),
		},
		{
			name:    "a phrase captioning a sticker",
			armKey:  undoKey{channelID: "channel-1", authorID: "author-1"},
			message: withSticker(undoMessage("channel-1", "author-1", "ei")),
		},
		{
			name:    "no window open for this author",
			armKey:  undoKey{channelID: "channel-1", authorID: "author-2"},
			message: undoMessage("channel-1", "author-1", "no"),
		},
		{
			name:    "the window is open in another channel",
			armKey:  undoKey{channelID: "channel-2", authorID: "author-1"},
			message: undoMessage("channel-1", "author-1", "ei"),
		},
		{
			name:    "no window open at all",
			armKey:  undoKey{channelID: "channel-9", authorID: "author-9"},
			message: undoMessage("channel-1", "author-1", "del"),
		},
		{
			name:    "the window has expired",
			armKey:  undoKey{channelID: "channel-1", authorID: "author-1"},
			expired: true,
			message: undoMessage("channel-1", "author-1", "del"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windows := isolateTriggerUndoWindows(t)

			armedAt := time.Now()
			if tt.expired {
				armedAt = armedAt.Add(-triggerUndoWindow)
			}
			windows.arm(tt.armKey, "response-1", armedAt)

			if handleTriggerUndo(nil, tt.message) {
				t.Fatal("handleTriggerUndo reported an undo it must not have consumed")
			}

			if tt.expired {
				return
			}
			if _, ok := windows.consume(tt.armKey, time.Now()); !ok {
				t.Error("the open window was taken by a message that must not have consumed it")
			}
		})
	}
}
