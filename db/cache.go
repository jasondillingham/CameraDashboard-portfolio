package db

import (
	"sync"
	"time"
)

// cachedData is a generic read-through cache with TTL.
// It stores a value of type T, reloading via loadFn when the TTL expires.
// Uses singleflight semantics: when the cache expires, only one goroutine
// reloads while others wait, preventing thundering-herd stampedes.
type cachedData[T any] struct {
	mu       sync.RWMutex
	data     T
	loaded   bool
	loadedAt time.Time
	ttl      time.Duration
	loadFn   func() (T, error)

	// singleflight: only one reload in-flight at a time
	reloadMu sync.Mutex
}

// newCache creates a new cachedData with the given TTL and load function.
func newCache[T any](ttl time.Duration, loadFn func() (T, error)) *cachedData[T] {
	return &cachedData[T]{
		ttl:    ttl,
		loadFn: loadFn,
	}
}

// get returns the cached value, reloading if expired or not yet loaded.
// When the cache expires, only one goroutine performs the reload (singleflight).
// Other concurrent callers get the stale value while the reload is in progress.
func (c *cachedData[T]) get() (T, error) {
	c.mu.RLock()
	if c.loaded && time.Since(c.loadedAt) < c.ttl {
		data := c.data
		c.mu.RUnlock()
		return data, nil
	}
	hasStale := c.loaded
	stale := c.data
	c.mu.RUnlock()

	// Try to become the single reloader; if another goroutine is already
	// reloading, return stale data (if any) instead of blocking.
	if !c.reloadMu.TryLock() {
		if hasStale {
			return stale, nil
		}
		// No stale data — must wait for the reload to finish.
		c.reloadMu.Lock()
		c.reloadMu.Unlock()
		c.mu.RLock()
		data := c.data
		c.mu.RUnlock()
		return data, nil
	}
	defer c.reloadMu.Unlock()

	// Double-check: another goroutine may have refreshed while we waited.
	c.mu.RLock()
	if c.loaded && time.Since(c.loadedAt) < c.ttl {
		data := c.data
		c.mu.RUnlock()
		return data, nil
	}
	c.mu.RUnlock()

	data, err := c.loadFn()
	if err != nil {
		// On error, serve stale data if available rather than failing every caller.
		if hasStale {
			return stale, nil
		}
		var zero T
		return zero, err
	}

	c.mu.Lock()
	c.data = data
	c.loaded = true
	c.loadedAt = time.Now()
	c.mu.Unlock()

	return data, nil
}

// invalidate clears the cache, forcing a reload on the next get().
func (c *cachedData[T]) invalidate() {
	c.mu.Lock()
	c.loaded = false
	c.mu.Unlock()
}
