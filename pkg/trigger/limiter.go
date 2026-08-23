package trigger

import (
	"sync"
	"time"
)

// ForcedLimiter rate-limits forced fires to one per author per ForcedInterval,
// so that mentioning the bot cannot be used to spam a channel.
//
// It is safe for concurrent use.
type ForcedLimiter struct {
	now func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// NewForcedLimiter returns a limiter reading the current time from now.
// Passing nil uses time.Now.
func NewForcedLimiter(now func() time.Time) *ForcedLimiter {
	if now == nil {
		now = time.Now
	}

	return &ForcedLimiter{
		now:  now,
		last: make(map[string]time.Time),
	}
}

// Allow reports whether authorID may force a fire, recording the attempt when
// it may. An empty authorID is always refused: an unattributable forced fire
// cannot be rate limited.
func (l *ForcedLimiter) Allow(authorID string) bool {
	if authorID == "" {
		return false
	}

	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Strictly less than: refusing does NOT extend the window, so spamming
	// does not push the next allowed fire further out.
	if last, ok := l.last[authorID]; ok && now.Sub(last) < ForcedInterval {
		return false
	}

	l.last[authorID] = now
	return true
}

// Prune drops entries older than ForcedInterval, bounding the map. Callers run
// it periodically; Allow does not, so that it stays O(1).
func (l *ForcedLimiter) Prune() {
	cutoff := l.now().Add(-ForcedInterval)

	l.mu.Lock()
	defer l.mu.Unlock()

	for author, last := range l.last {
		if last.Before(cutoff) {
			delete(l.last, author)
		}
	}
}
