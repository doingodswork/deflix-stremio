package cinemata

import (
	"sync"
	"time"
)

// CacheItem combines a movie object and a creation time in a single struct.
// This can be useful for implementing the Cache interface, but is not necessarily required.
// See the InMemoryCache example implementation of the Cache interface for its usage.
type CacheItem struct {
	Movie   Movie
	Created time.Time
}

// Cache is the interface that the cinemata client uses for caching movie.
// A package user must pass an implementation of this interface.
// Usually you create a simple wrapper around an existing cache package.
// An example implementation is the InMemoryCache in this package.
type Cache interface {
	Set(key string, movie Movie) error
	Get(key string) (Movie, time.Time, bool, error)
}

var _ Cache = (*InMemoryCache)(nil)

// InMemoryCache is an example implementation of the Cache interface.
// It doesn't persist its data, so it's not suited for production use of the cinemata package.
type InMemoryCache struct {
	cache map[string]CacheItem
	lock  *sync.RWMutex
}

// Set stores a movie object and the current time in the cache.
func (c InMemoryCache) Set(key string, movie Movie) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.cache[key] = CacheItem{
		Movie:   movie,
		Created: time.Now(),
	}
	return nil
}

// Get returns a movie object and the time it was cached from the cache.
// The boolean return value signals if the value was found in the cache.
func (c InMemoryCache) Get(key string) (Movie, time.Time, bool, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	cacheItem, found := c.cache[key]
	return cacheItem.Movie, cacheItem.Created, found, nil
}
