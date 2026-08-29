package trigger

import (
	"context"
	"sync"
)

type Loader func(ctx context.Context, instanceID int64) ([]Candidate, error)

// Cache holds compiled candidate sets per instance. Safe for concurrent use.
type Cache struct {
	load Loader

	mu  sync.Mutex
	set map[int64][]Candidate
}

func NewCache(load Loader) *Cache {
	return &Cache{
		load: load,
		set:  make(map[int64][]Candidate),
	}
}

// Candidates returns the compiled set for an instance, loading it on a miss.
// The returned slice is shared with every other caller and must not be mutated.
func (c *Cache) Candidates(ctx context.Context, instanceID int64) ([]Candidate, error) {
	// The lock is held across the load so concurrent misses cannot both load.
	c.mu.Lock()
	defer c.mu.Unlock()

	if cached, ok := c.set[instanceID]; ok {
		return cached, nil
	}

	loaded, err := c.load(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	// An empty result is cached too, or every message reloads it.
	c.set[instanceID] = loaded
	return loaded, nil
}

func (c *Cache) Invalidate(instanceID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.set, instanceID)
}

func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.set = make(map[int64][]Candidate)
}
