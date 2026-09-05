package discord

import (
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// See ADR-0037.
const triggerUndoWindow = 7 * time.Second

// Matched against the whole trimmed message, so "no" undoes and "no idea" does
// not.
var triggerUndoPhrases = map[string]struct{}{
	"no":  {},
	"ei":  {},
	"del": {},
}

func isTriggerUndoPhrase(content string) bool {
	_, ok := triggerUndoPhrases[strings.ToLower(strings.TrimSpace(content))]

	return ok
}

type undoKey struct {
	channelID string
	authorID  string
}

type undoEntry struct {
	responseID string
	expiresAt  time.Time
}

// Every operation prunes, so a window nobody uses cannot accumulate.
type undoWindows struct {
	mu      sync.Mutex
	pending map[undoKey]undoEntry
}

func newUndoWindows() *undoWindows {
	return &undoWindows{pending: make(map[undoKey]undoEntry)}
}

// arm replaces any window already open for the key: the latest response is the
// one an undo means.
func (w *undoWindows) arm(key undoKey, responseID string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pruneLocked(now)
	w.pending[key] = undoEntry{responseID: responseID, expiresAt: now.Add(triggerUndoWindow)}
}

func (w *undoWindows) consume(key undoKey, now time.Time) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pruneLocked(now)

	entry, ok := w.pending[key]
	if !ok {
		return "", false
	}
	delete(w.pending, key)

	return entry.responseID, true
}

func (w *undoWindows) pruneLocked(now time.Time) {
	for key, entry := range w.pending {
		if !now.Before(entry.expiresAt) {
			delete(w.pending, key)
		}
	}
}

// discordgo dispatches every MessageCreate on its own goroutine, so this is
// read and written concurrently.
var triggerUndoWindows = newUndoWindows()

// A nil sent message means the send failed, leaving nothing to undo.
func armTriggerUndo(m *discordgo.MessageCreate, sent *discordgo.Message) {
	if m == nil || m.Author == nil || sent == nil {
		return
	}

	triggerUndoWindows.arm(
		undoKey{channelID: m.ChannelID, authorID: m.Author.ID}, sent.ID, time.Now())
}

// handleTriggerUndo reports whether the message cancelled a fired trigger, in
// which case it is consumed and must not be processed any further.
func handleTriggerUndo(s *discordgo.Session, m *discordgo.MessageCreate) bool {
	if m == nil || m.Author == nil || !isTriggerUndoPhrase(m.Content) {
		return false
	}

	// An attachment captioned "no" is a post, not an undo. Without this it
	// would be deleted, and would never reach attemptRepost either.
	if len(m.Attachments) > 0 || len(m.StickerItems) > 0 {
		return false
	}

	responseID, ok := triggerUndoWindows.consume(
		undoKey{channelID: m.ChannelID, authorID: m.Author.ID}, time.Now())
	if !ok {
		return false
	}

	if err := s.ChannelMessageDelete(m.ChannelID, responseID); err != nil {
		// The response survived, so the undo message stays rather than being
		// deleted for nothing.
		log.Z.Warn("failed to delete a trigger response on undo.", zap.Error(err))
		return true
	}

	// Deleting someone else's message needs MANAGE_MESSAGES, which a guild need
	// not have granted; Debug so an ungranted one does not fill the log.
	if err := s.ChannelMessageDelete(m.ChannelID, m.ID); err != nil {
		log.Z.Debug("failed to delete an undo message.", zap.Error(err))
	}

	return true
}
