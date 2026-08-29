package trigger

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a controllable time source, advanced only by the test.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestForcedLimiterFirstCallAllowed(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	if !limiter.Allow("author-1") {
		t.Error("first Allow for a fresh author = false, want true")
	}
}

func TestForcedLimiterImmediateSecondCallRefused(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	limiter.Allow("author-1")
	if limiter.Allow("author-1") {
		t.Error("immediate second Allow = true, want false (within ForcedInterval)")
	}
}

func TestForcedLimiterAllowsAtExactlyTheInterval(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	if !limiter.Allow("author-1") {
		t.Fatal("first Allow = false, want true")
	}

	clock.Advance(ForcedInterval)
	if !limiter.Allow("author-1") {
		t.Error("Allow exactly ForcedInterval later = false, want true")
	}
}

func TestForcedLimiterJustBeforeTheIntervalIsRefused(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	limiter.Allow("author-1")
	clock.Advance(ForcedInterval - time.Nanosecond)
	if limiter.Allow("author-1") {
		t.Error("Allow one nanosecond before ForcedInterval = true, want false")
	}
}

func TestForcedLimiterRefusalDoesNotExtendTheWindow(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	if !limiter.Allow("author-1") {
		t.Fatal("t=0 Allow = false, want true")
	}

	clock.Advance(30 * time.Second)
	if limiter.Allow("author-1") {
		t.Fatal("t=30s Allow = true, want false")
	}

	clock.Advance(30 * time.Second) // now at t=60s
	if !limiter.Allow("author-1") {
		t.Error("t=60s Allow = false, want true; the refused attempt at t=30s must not have extended the window")
	}
}

func TestForcedLimiterEmptyAuthorIDAlwaysRefused(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	for i := range 3 {
		if limiter.Allow("") {
			t.Errorf("Allow(\"\") call #%d = true, want false always", i)
		}
	}
}

func TestForcedLimiterAuthorsDoNotInterfere(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	if !limiter.Allow("author-a") {
		t.Fatal("author-a first Allow = false, want true")
	}
	if !limiter.Allow("author-b") {
		t.Error("author-b first Allow = false, want true (must not be blocked by author-a)")
	}
	if limiter.Allow("author-a") {
		t.Error("author-a second immediate Allow = true, want false")
	}
}

func TestNewForcedLimiterNilUsesTimeNow(t *testing.T) {
	limiter := NewForcedLimiter(nil)

	if !limiter.Allow("author-1") {
		t.Error("first Allow with the default clock = false, want true")
	}
}

func TestForcedLimiterConcurrentAllowIsRaceFree(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	const authors = 64
	results := make([]bool, authors)

	var wg sync.WaitGroup
	for i := range authors {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			authorID := "concurrent-author-" + string(rune('A'+i%26)) + string(rune('0'+i/26))
			results[i] = limiter.Allow(authorID)
		}(i)
	}
	wg.Wait()

	for i, ok := range results {
		if !ok {
			t.Errorf("goroutine %d: first Allow for its own author = false, want true", i)
		}
	}
}

func TestForcedLimiterConcurrentAllowForTheSameAuthorAllowsExactlyOnce(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	const attempts = 50
	var allowedCount int32
	var mu sync.Mutex

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for range attempts {
		done.Go(func() {
			start.Wait()
			if limiter.Allow("same-author") {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		})
	}
	start.Done()
	done.Wait()

	if allowedCount != 1 {
		t.Errorf("allowed count = %d across %d concurrent attempts for one author, want exactly 1", allowedCount, attempts)
	}
}

func TestForcedLimiterPruneDoesNotAffectAnAllowDecision(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	limiter.Allow("author-1")
	clock.Advance(ForcedInterval + time.Second) // entry is now stale
	limiter.Prune()

	if !limiter.Allow("author-1") {
		t.Error("Allow after the interval elapsed and Prune ran = false, want true")
	}
}
