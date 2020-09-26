package debrid

import (
	"sync"
	"time"
)

// Cache is the interface that the debrid clients uses for caching a user's API token validity and the "instant availability" of a torrent (via info_hash).
// A package user must pass an implementation of this interface.
// Usually you create a simple wrapper around an existing cache package.
// An example implementation is the InMemoryCache in this package.
type Cache interface {
	Set(key string) error
	Get(key string) (time.Time, bool, error)
}

var _ Cache = (*InMemoryCache)(nil)

// InMemoryCache is an example implementation of the Cache interface.
// It doesn't persist its data, so it's not suited for production use of the debrid packages.
type InMemoryCache struct {
	cache map[string]time.Time
	lock  *sync.RWMutex
}

// NewInMemoryCache creates a new InMemoryCache.
func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{
		cache: map[string]time.Time{},
		lock:  &sync.RWMutex{},
	}
}

// Set caches the validity of a user's API token or the "instant availability" for a torrent (via info_hash).
// There's no need to pass a boolean or so - if a value gets cached it means the token is valid / the torrent is "instantly available".
func (c *InMemoryCache) Set(key string) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.cache[key] = time.Now()
	return nil
}

// Get returns the time the API token / "instant availability" was cached.
// The boolean return value signals if the value was found in the cache.
func (c *InMemoryCache) Get(key string) (time.Time, bool, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	created, found := c.cache[key]
	return created, found, nil
}
