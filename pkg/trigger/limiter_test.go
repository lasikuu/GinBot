package trigger

import (
	"sync"
	"testing"
	"time"
)

// ── Assumed symbols from pkg/trigger (spec §7.7) ─────────────────────────────
//
//	const ForcedInterval = 60 * time.Second
//
//	type ForcedLimiter struct { /* unexported fields */ }
//
//	func NewForcedLimiter(now func() time.Time) *ForcedLimiter
//	func (l *ForcedLimiter) Allow(authorID string) bool
//	func (l *ForcedLimiter) Prune()
//
// The first Allow for an author succeeds; a second strictly before
// ForcedInterval has elapsed is refused AND DOES NOT extend the window; Allow
// at exactly ForcedInterval after the recorded fire succeeds; an empty
// authorID is always refused; two authors do not interfere. Safe for
// concurrent use. None of these exist yet; pkg/trigger does not exist as of
// this writing.

// fakeClock is a controllable time source for NewForcedLimiter, advanced only
// by the test — never by wall-clock time, so the limiter's tests are
// deterministic.
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

// TestForcedLimiterFirstCallAllowed.
func TestForcedLimiterFirstCallAllowed(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	if !limiter.Allow("author-1") {
		t.Error("first Allow for a fresh author = false, want true")
	}
}

// TestForcedLimiterImmediateSecondCallRefused.
func TestForcedLimiterImmediateSecondCallRefused(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	limiter.Allow("author-1")
	if limiter.Allow("author-1") {
		t.Error("immediate second Allow = true, want false (within ForcedInterval)")
	}
}

// TestForcedLimiterAllowsAtExactlyTheInterval.
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

// TestForcedLimiterJustBeforeTheIntervalIsRefused pins the other side of the
// same boundary.
func TestForcedLimiterJustBeforeTheIntervalIsRefused(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	limiter.Allow("author-1")
	clock.Advance(ForcedInterval - time.Nanosecond)
	if limiter.Allow("author-1") {
		t.Error("Allow one nanosecond before ForcedInterval = true, want false")
	}
}

// TestForcedLimiterRefusalDoesNotExtendTheWindow: spamming Allow must not push
// the next allowed fire further out than the FIRST recorded fire. t=0 allowed,
// t=30s refused, t=60s allowed proves the window is anchored to t=0, not to the
// refused attempt at t=30s (which would put the next allowed fire at t=90s).
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

// TestForcedLimiterEmptyAuthorIDAlwaysRefused: an unattributable forced fire
// cannot be rate limited, so it is refused unconditionally rather than
// treated as its own (shared) bucket.
func TestForcedLimiterEmptyAuthorIDAlwaysRefused(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	for i := 0; i < 3; i++ {
		if limiter.Allow("") {
			t.Errorf("Allow(\"\") call #%d = true, want false always", i)
		}
	}
}

// TestForcedLimiterAuthorsDoNotInterfere: one author being rate limited must
// not affect another's independent window.
func TestForcedLimiterAuthorsDoNotInterfere(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	if !limiter.Allow("author-a") {
		t.Fatal("author-a first Allow = false, want true")
	}
	if !limiter.Allow("author-b") {
		t.Error("author-b first Allow = false, want true (must not be blocked by author-a)")
	}
	// author-a is now within its own window and must still be refused.
	if limiter.Allow("author-a") {
		t.Error("author-a second immediate Allow = true, want false")
	}
}

// TestNewForcedLimiterNilUsesTimeNow: passing nil must not panic and must fall
// back to a working clock (time.Now), per the doc comment on NewForcedLimiter.
func TestNewForcedLimiterNilUsesTimeNow(t *testing.T) {
	limiter := NewForcedLimiter(nil)

	if !limiter.Allow("author-1") {
		t.Error("first Allow with the default clock = false, want true")
	}
}

// TestForcedLimiterConcurrentAllowIsRaceFree runs Allow from many goroutines so
// `go test -race` exercises the limiter's shared state. It asserts something
// concrete rather than merely "did not crash": across N authors called
// concurrently exactly once each, every first call must succeed.
func TestForcedLimiterConcurrentAllowIsRaceFree(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	const authors = 64
	results := make([]bool, authors)

	var wg sync.WaitGroup
	for i := 0; i < authors; i++ {
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

// TestForcedLimiterConcurrentAllowForTheSameAuthorAllowsExactlyOnce: many
// goroutines racing Allow for the SAME author, all at the same instant, must
// let exactly one through — the rate limiter's entire purpose.
func TestForcedLimiterConcurrentAllowForTheSameAuthorAllowsExactlyOnce(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	limiter := NewForcedLimiter(clock.Now)

	const attempts = 50
	var allowedCount int32
	var mu sync.Mutex

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < attempts; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			if limiter.Allow("same-author") {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}
	start.Done()
	done.Wait()

	if allowedCount != 1 {
		t.Errorf("allowed count = %d across %d concurrent attempts for one author, want exactly 1", allowedCount, attempts)
	}
}

// TestForcedLimiterPruneDoesNotAffectAnAllowDecision: Prune bounds the map for
// callers that run it periodically, but Allow's own decision must be
// unaffected by whether Prune has run — Prune is a housekeeping call, not part
// of the rate-limit contract itself for an author still within the window.
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
