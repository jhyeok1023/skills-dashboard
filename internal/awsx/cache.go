package awsx

import (
	"context"
	"sync"
	"time"
)

// Cache memoises expensive AWS calls for a short window and collapses
// concurrent misses into a single call.
//
// Two things here are direct answers to how the reference implementation's
// cache behaved under load. It released its lock before invoking the loader, so
// every concurrent miss issued its own AWS call — a stampede that arrived
// exactly when the dashboard was busiest. And it never stored failures, so a
// throttled or failing dependency was re-attempted at full rate on every tick,
// which is how a transient error became a sustained one.
type Cache struct {
	// TTL is how long a successful value is served from memory.
	TTL time.Duration
	// ErrorTTL is how long a failure is remembered. Short, but non-zero: long
	// enough to stop a retry storm, short enough that a recovered dependency
	// is noticed quickly.
	ErrorTTL time.Duration
	// Now is overridable in tests.
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	done    chan struct{}
	val     any
	err     error
	expires time.Time
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) ttl() time.Duration {
	if c.TTL <= 0 {
		return 30 * time.Second
	}
	return c.TTL
}

func (c *Cache) errorTTL() time.Duration {
	if c.ErrorTTL <= 0 {
		return 5 * time.Second
	}
	return c.ErrorTTL
}

// Do returns the cached value for key, calling load at most once across all
// concurrent callers that miss.
func (c *Cache) Do(ctx context.Context, key string, load func(context.Context) (any, error)) (any, error) {
	for {
		c.mu.Lock()
		if c.entries == nil {
			c.entries = map[string]*cacheEntry{}
		}
		c.prune()

		if e, ok := c.entries[key]; ok {
			c.mu.Unlock()
			select {
			case <-e.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// The entry may have expired while this caller waited on it, in
			// which case start again rather than serve something stale.
			c.mu.Lock()
			fresh := c.entries[key] == e && c.now().Before(e.expires)
			c.mu.Unlock()
			if fresh {
				return e.val, e.err
			}
			c.mu.Lock()
			if c.entries[key] == e {
				delete(c.entries, key)
			}
			c.mu.Unlock()
			continue
		}

		// Claim the key before releasing the lock. This is the whole point:
		// every other caller now finds this entry and waits on it instead of
		// issuing its own call.
		e := &cacheEntry{done: make(chan struct{}), expires: c.now().Add(c.ttl())}
		c.entries[key] = e
		c.mu.Unlock()

		e.val, e.err = load(ctx)

		c.mu.Lock()
		if e.err != nil {
			e.expires = c.now().Add(c.errorTTL())
		} else {
			e.expires = c.now().Add(c.ttl())
		}
		c.mu.Unlock()
		close(e.done)

		return e.val, e.err
	}
}

// prune drops expired entries. The cache is keyed by request shape, which is
// bounded in practice, but an unbounded map that is never swept is a leak
// waiting for a new key format.
func (c *Cache) prune() {
	now := c.now()
	for k, e := range c.entries {
		select {
		case <-e.done:
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		default:
			// still loading; leave it for its callers
		}
	}
}

// Invalidate drops everything, which the settings page does after a save so the
// next read reflects the new resource selection immediately.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
}

// Len reports how many entries are held, for tests and diagnostics.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Cached is a typed wrapper over Cache.Do.
func Cached[T any](ctx context.Context, c *Cache, key string, load func(context.Context) (T, error)) (T, error) {
	var zero T
	v, err := c.Do(ctx, key, func(ctx context.Context) (any, error) {
		return load(ctx)
	})
	if err != nil {
		return zero, err
	}
	typed, ok := v.(T)
	if !ok {
		return zero, nil
	}
	return typed, nil
}
