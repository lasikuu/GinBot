package trigger

import (
	"sync"
	"time"
)

// ForcedLimiter allows one forced fire per author per ForcedInterval. Safe for
// concurrent use.
type ForcedLimiter struct {
	now func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// NewForcedLimiter reads the current time from now; nil means time.Now.
func NewForcedLimiter(now func() time.Time) *ForcedLimiter {
	if now == nil {
		now = time.Now
	}

	return &ForcedLimiter{
		now:  now,
		last: make(map[string]time.Time),
	}
}

// Allow records the attempt when it permits it. An empty authorID is always
// refused, being unattributable.
func (l *ForcedLimiter) Allow(authorID string) bool {
	if authorID == "" {
		return false
	}

	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	// A refusal does not record, so spamming cannot extend the window.
	if last, ok := l.last[authorID]; ok && now.Sub(last) < ForcedInterval {
		return false
	}

	l.last[authorID] = now
	return true
}

// Prune must be run periodically by the caller; Allow never does, to stay O(1).
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
