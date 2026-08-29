package trigger

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type countingLoader struct {
	calls atomic.Int32
	fn    func(ctx context.Context, instanceID int64) ([]Candidate, error)
}

func (l *countingLoader) load(ctx context.Context, instanceID int64) ([]Candidate, error) {
	l.calls.Add(1)
	return l.fn(ctx, instanceID)
}

func (l *countingLoader) count() int {
	return int(l.calls.Load())
}

var errLoadFailed = errors.New("simulated load failure")

func TestCacheMissCallsLoaderHitDoesNot(t *testing.T) {
	loader := &countingLoader{fn: func(context.Context, int64) ([]Candidate, error) {
		return []Candidate{{ID: "t1"}}, nil
	}}
	cache := NewCache(loader.load)

	if _, err := cache.Candidates(context.Background(), 1); err != nil {
		t.Fatalf("first Candidates call: %v", err)
	}
	if got := loader.count(); got != 1 {
		t.Fatalf("loader calls after the first (miss) call = %d, want 1", got)
	}

	if _, err := cache.Candidates(context.Background(), 1); err != nil {
		t.Fatalf("second Candidates call: %v", err)
	}
	if got := loader.count(); got != 1 {
		t.Errorf("loader calls after the second (hit) call = %d, want still 1", got)
	}
}

func TestCacheLoadErrorIsNotCached(t *testing.T) {
	loader := &countingLoader{fn: func(context.Context, int64) ([]Candidate, error) {
		return nil, errLoadFailed
	}}
	cache := NewCache(loader.load)

	if _, err := cache.Candidates(context.Background(), 1); !errors.Is(err, errLoadFailed) {
		t.Fatalf("first call err = %v, want errLoadFailed", err)
	}
	if _, err := cache.Candidates(context.Background(), 1); !errors.Is(err, errLoadFailed) {
		t.Fatalf("second call err = %v, want errLoadFailed (retried, not cached-as-failed)", err)
	}
	if got := loader.count(); got != 2 {
		t.Errorf("loader calls = %d, want 2 (both calls retried the load)", got)
	}
}

func TestCacheEmptyResultIsCached(t *testing.T) {
	loader := &countingLoader{fn: func(context.Context, int64) ([]Candidate, error) {
		return []Candidate{}, nil
	}}
	cache := NewCache(loader.load)

	for i := range 3 {
		got, err := cache.Candidates(context.Background(), 1)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(got) != 0 {
			t.Fatalf("call %d: got %d candidates, want 0", i, len(got))
		}
	}
	if got := loader.count(); got != 1 {
		t.Errorf("loader calls = %d across 3 requests for an empty result, want 1", got)
	}
}

func TestCacheInvalidateDropsOneInstanceLeavesOthers(t *testing.T) {
	loader := &countingLoader{fn: func(_ context.Context, instanceID int64) ([]Candidate, error) {
		return []Candidate{{ID: "t-" + string(rune('0'+instanceID))}}, nil
	}}
	cache := NewCache(loader.load)

	if _, err := cache.Candidates(context.Background(), 1); err != nil {
		t.Fatalf("warm instance 1: %v", err)
	}
	if _, err := cache.Candidates(context.Background(), 2); err != nil {
		t.Fatalf("warm instance 2: %v", err)
	}
	if got := loader.count(); got != 2 {
		t.Fatalf("loader calls after warming two instances = %d, want 2", got)
	}

	cache.Invalidate(1)

	if _, err := cache.Candidates(context.Background(), 1); err != nil {
		t.Fatalf("instance 1 after invalidate: %v", err)
	}
	if got := loader.count(); got != 3 {
		t.Errorf("loader calls after invalidated instance 1 was re-requested = %d, want 3", got)
	}

	if _, err := cache.Candidates(context.Background(), 2); err != nil {
		t.Fatalf("instance 2 after invalidating instance 1: %v", err)
	}
	if got := loader.count(); got != 3 {
		t.Errorf("loader calls after untouched instance 2 was re-requested = %d, want still 3 (it must still be cached)", got)
	}
}

func TestCacheInvalidateAllDropsEverything(t *testing.T) {
	loader := &countingLoader{fn: func(context.Context, int64) ([]Candidate, error) {
		return []Candidate{{ID: "t"}}, nil
	}}
	cache := NewCache(loader.load)

	for _, instanceID := range []int64{1, 2, 3} {
		if _, err := cache.Candidates(context.Background(), instanceID); err != nil {
			t.Fatalf("warm instance %d: %v", instanceID, err)
		}
	}
	if got := loader.count(); got != 3 {
		t.Fatalf("loader calls after warming three instances = %d, want 3", got)
	}

	cache.InvalidateAll()

	for _, instanceID := range []int64{1, 2, 3} {
		if _, err := cache.Candidates(context.Background(), instanceID); err != nil {
			t.Fatalf("instance %d after InvalidateAll: %v", instanceID, err)
		}
	}
	if got := loader.count(); got != 6 {
		t.Errorf("loader calls after re-requesting all three post-InvalidateAll = %d, want 6", got)
	}
}

// TestCacheConcurrentCandidatesAndInvalidate cannot assert the loader count,
// which is racy against concurrent Invalidate by design.
func TestCacheConcurrentCandidatesAndInvalidate(t *testing.T) {
	loader := &countingLoader{fn: func(context.Context, int64) ([]Candidate, error) {
		return []Candidate{{ID: "t"}}, nil
	}}
	cache := NewCache(loader.load)

	const goroutines = 32
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*opsPerGoroutine)

	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range opsPerGoroutine {
				instanceID := int64((g + i) % 8)
				switch i % 3 {
				case 0:
					cache.Invalidate(instanceID)
				case 1:
					cache.InvalidateAll()
				default:
					if _, err := cache.Candidates(context.Background(), instanceID); err != nil {
						errs <- err
					}
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("unexpected error under concurrent access: %v", err)
	}
}
