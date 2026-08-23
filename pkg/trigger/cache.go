package trigger

import (
	"context"
	"sync"
)

// Loader fetches the compiled candidate set for one instance.
type Loader func(ctx context.Context, instanceID int64) ([]Candidate, error)

// Cache holds compiled candidate sets per instance, so that matching a message
// costs no database round trip. Writes to a trigger invalidate the affected
// instances.
//
// It is safe for concurrent use.
type Cache struct {
	load Loader

	mu  sync.Mutex
	set map[int64][]Candidate
}

// NewCache returns a cache backed by load.
func NewCache(load Loader) *Cache {
	return &Cache{
		load: load,
		set:  make(map[int64][]Candidate),
	}
}

// Candidates returns the compiled set for an instance, loading it on a miss.
//
// The returned slice must be treated as read-only: it is shared with every
// other caller and with the cache.
func (c *Cache) Candidates(ctx context.Context, instanceID int64) ([]Candidate, error) {
	// The lock is held across the load call rather than released and
	// reacquired around it: a caller blocking on another's in-flight load is a
	// simpler, safer trade than both racing to populate the same entry.
	c.mu.Lock()
	defer c.mu.Unlock()

	if cached, ok := c.set[instanceID]; ok {
		return cached, nil
	}

	loaded, err := c.load(ctx, instanceID)
	if err != nil {
		// A load error is not cached: the next call retries.
		return nil, err
	}

	// An empty result is cached deliberately: a map entry with a nil or empty
	// slice still reports ok on lookup, so an instance with no triggers does
	// not hit the database on every message.
	c.set[instanceID] = loaded
	return loaded, nil
}

// Invalidate drops the cached set for one instance.
func (c *Cache) Invalidate(instanceID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.set, instanceID)
}

// InvalidateAll drops every cached set.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.set = make(map[int64][]Candidate)
}
