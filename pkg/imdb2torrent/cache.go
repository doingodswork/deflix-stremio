package imdb2torrent

import (
	"sync"
	"time"
)

// CacheItem combines Result objects and a creation time in a single struct.
// This can be useful for implementing the Cache interface, but is not necessarily required.
// See the InMemoryCache example implementation of the Cache interface for its usage.
type CacheItem struct {
	Results []Result
	Created time.Time
}

// Cache is the interface that the imdb2torrent clients use for caching results.
// A package user must pass an implementation of this interface.
// Usually you create a simple wrapper around an existing cache package.
// An example implementation is the InMemoryCache in this package.
type Cache interface {
	Set(key string, results []Result) error
	Get(key string) ([]Result, time.Time, bool, error)
}

var _ Cache = (*InMemoryCache)(nil)

// InMemoryCache is an example implementation of the Cache interface.
// It doesn't persist its data, so it's not suited for production use of the imdb2torrent package.
type InMemoryCache struct {
	cache map[string]CacheItem
	lock  *sync.RWMutex
}

// NewInMemoryCache creates a new InMemoryCache.
func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{
		cache: map[string]CacheItem{},
		lock:  &sync.RWMutex{},
	}
}

// Set stores Result objects and the current time in the cache.
func (c *InMemoryCache) Set(key string, results []Result) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.cache[key] = CacheItem{
		Results: results,
		Created: time.Now(),
	}
	return nil
}

// Get returns Result objects and the time they were cached from the cache.
// The boolean return value signals if the value was found in the cache.
func (c *InMemoryCache) Get(key string) ([]Result, time.Time, bool, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	cacheItem, found := c.cache[key]
	return cacheItem.Results, cacheItem.Created, found, nil
}
