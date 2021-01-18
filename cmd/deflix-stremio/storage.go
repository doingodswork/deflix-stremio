package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/dgraph-io/badger/v2"
	"github.com/go-redis/redis/v8"
	gocache "github.com/patrickmn/go-cache"
	"go.uber.org/zap"

	"github.com/deflix-tv/go-debrid"
	"github.com/deflix-tv/go-stremio/pkg/cinemeta"
	"github.com/deflix-tv/imdb2torrent"
)

func registerTypes() {
	// For RealDebrid availability and token cache
	gob.Register(time.Time{})
	// For cinemeta cache
	gob.Register(cinemeta.CacheItem{})
	// For redirect cache
	gob.Register([]imdb2torrent.Result{})
	// For stream cache
	gob.Register(cacheItem{})
}

type cacheItem struct {
	Value   string
	Created time.Time
}

var _ imdb2torrent.Cache = (*resultStore)(nil)

// resultStore is the store for imdb2torrent.Result objects, backed by BadgerDB.
type resultStore struct {
	db        *badger.DB
	keyPrefix string
}

// Set implements the imdb2torrent.Cache interface.
func (c *resultStore) Set(key string, results []imdb2torrent.Result) error {
	item := imdb2torrent.CacheItem{
		Results: results,
		Created: time.Now(),
	}
	return gobSet(c.db, c.keyPrefix+key, item)
}

// Get implements the imdb2torrent.Cache interface.
func (c *resultStore) Get(key string) ([]imdb2torrent.Result, time.Time, bool, error) {
	var item imdb2torrent.CacheItem
	found, err := gobGet(c.db, c.keyPrefix+key, &item)
	return item.Results, item.Created, found, err
}

var _ cinemeta.Cache = (*metaStore)(nil)

// metaStore is the store for cinemeta.Meta objects, backed by BadgerDB.
type metaStore struct {
	db        *badger.DB
	keyPrefix string
}

// Set implements the cinemeta.Cache interface.
func (c *metaStore) Set(key string, meta cinemeta.Meta) error {
	item := cinemeta.CacheItem{
		Meta:    meta,
		Created: time.Now(),
	}
	return gobSet(c.db, c.keyPrefix+key, item)
}

// Get implements the cinemeta.Cache interface.
func (c *metaStore) Get(key string) (cinemeta.Meta, time.Time, bool, error) {
	var item cinemeta.CacheItem
	found, err := gobGet(c.db, c.keyPrefix+key, &item)
	if err != nil {
		return cinemeta.Meta{}, time.Time{}, found, err
	} else if !found {
		return cinemeta.Meta{}, time.Time{}, found, nil
	}
	return item.Meta, item.Created, found, nil
}

var _ debrid.Cache = (*creationCache)(nil)

// creationCache caches if a key exists and the time this was cached.
type creationCache struct {
	cache *gocache.Cache
}

// Set implements the cinemeta.Cache interface.
func (c *creationCache) Set(key string) error {
	c.cache.Set(key, time.Now(), 0)
	return nil
}

// Get implements the cinemeta.Cache interface.
func (c *creationCache) Get(key string) (time.Time, bool, error) {
	createdIface, found := c.cache.Get(key)
	if !found {
		return time.Time{}, found, nil
	}
	created, ok := createdIface.(time.Time)
	if !ok {
		return time.Time{}, found, fmt.Errorf("Couldn't cast cached value to time.Time: type was: %T", createdIface)
	}
	return created, found, nil
}

var _ goCacher = (*goCache)(nil)

// goCache wraps both a go-cache instance and Redis and offers methods with the exact same signature as go-cache.
// If the Redis client is not nil, it's the one that's used exclusively. Otherwise go-cache is used.
// The data in this cache is meant to be temporary, while also important to be the same across multiple nodes.
// This is why there's no reason to for example read data from Redis and (during the same Get call) store the fetched data in go-cache to have a local copy in case of a Redis connection error, or to store data in both at the same time during a Set call.
type goCache struct {
	cache *gocache.Cache
	rdb   *redis.Client
	// Only required when using Redis. Must be the actual type. So if you have a pointer, set this to the "element" of the pointer.
	t reflect.Type
	// Only required when using Redis.
	logger *zap.Logger
}

func (c *goCache) Set(k string, v interface{}, d time.Duration) {
	if c.rdb != nil {
		// Note: We can only decode into a pointer. And when working with interfaces gob requires to encode a pointer.
		if b, err := toGob(&v); err != nil {
			c.logger.Error("Couldn't encode value as gob", zap.Error(err))
		} else if err := c.rdb.Set(context.Background(), k, b, d).Err(); err != nil {
			c.logger.Error("Couldn't set value in Redis", zap.Error(err))
		}
	} else {
		c.cache.Set(k, v, d)
	}
}

func (c *goCache) Get(k string) (interface{}, bool) {
	if c.rdb != nil {
		if v, err := c.rdb.Get(context.Background(), k).Result(); err != nil && err != redis.Nil {
			// Note: We only log this when there's an error *and* it's not `redis.Nil` (which just indicates that the value was not found).
			c.logger.Error("Couldn't get value from Redis", zap.Error(err))
			// Note: Don't return `nil, true` here, although that would be more correct. But given that the implementation is meant to have the same behavior as go-cache, where there are never encoding errors, a `nil, true` would lead to a caller assuming they can work with the value, but it's nil.
		} else if err != redis.Nil {
			var vi interface{}
			if c.t.Kind() == reflect.Slice {
				vi = reflect.MakeSlice(c.t, 0, 0)
			} else {
				vi = reflect.New(c.t)
			}
			if err := fromGob([]byte(v), &vi); err != nil {
				c.logger.Error("Couldn't decode gob", zap.Error(err))
			} else {
				return vi, true
			}
		}
		// Else: err == redis.Nil, which we don't need to explicitly handle, as returning `nil, false` is perfect for that.
		return nil, false
	} else {
		return c.cache.Get(k)
	}
}

func toGob(v interface{}) ([]byte, error) {
	writer := bytes.Buffer{}
	encoder := gob.NewEncoder(&writer)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return writer.Bytes(), nil
}

func fromGob(b []byte, v interface{}) error {
	reader := bytes.NewReader(b)
	decoder := gob.NewDecoder(reader)
	if err := decoder.Decode(v); err != nil {
		return err
	}
	return nil
}

func gobSet(db *badger.DB, key string, item interface{}) error {
	b, err := toGob(item)
	if err != nil {
		return fmt.Errorf("Couldn't encode item: %v", err)
	}
	return db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), b)
	})
}

func gobGet(db *badger.DB, key string, target interface{}) (bool, error) {
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		item.Value(func(val []byte) error {
			return fromGob(val, target)
		})
		return nil
	})
	if err == badger.ErrKeyNotFound {
		return false, nil
	} else if err != nil {
		return true, err
	}
	return true, nil
}

func saveGoCache(items map[string]gocache.Item, filePath string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("Couldn't create go-cache file: %v", err)
	}
	encoder := gob.NewEncoder(file)
	if err = encoder.Encode(items); err != nil {
		return fmt.Errorf("Couldn't encode items for go-cache file: %v", err)
	}
	return nil
}

func loadGoCache(filePath string) (map[string]gocache.Item, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("Couldn't open go-cache file: %v", err)
	}
	decoder := gob.NewDecoder(file)
	result := map[string]gocache.Item{}
	if err = decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("Couldn't decode items from go-cache file: %v", err)
	}
	return result, nil
}

func persistCaches(ctx context.Context, cacheFilePath string, goCaches map[string]*gocache.Cache, logger *zap.Logger) {
	// TODO: We might want to overthink this - persisting caches on shutdown might be useful, especially for the redirect cache!
	if ctx.Err() != nil {
		logger.Warn("Regular cache persistence triggered, but server is shutting down")
		return
	}

	logger.Info("Persisting caches...", zap.String("cacheFilePath", cacheFilePath))
	start := time.Now()

	// If the dir doesn't exist yet, we'll create it
	_, err := os.Stat(cacheFilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(cacheFilePath, os.ModeDir); err != nil {
				logger.Error("Couldn't create cache directory", zap.Error(err), zap.String("dir", cacheFilePath))
				return
			}
			logger.Info("Created cache directory", zap.String("dir", cacheFilePath))
		} else {
			logger.Error("Couldn't get cache directory info", zap.Error(err), zap.String("dir", cacheFilePath))
			return
		}
	}

	for name, goCache := range goCaches {
		if err := saveGoCache(goCache.Items(), cacheFilePath+"/"+name+".gob"); err != nil {
			logger.Error("Couldn't save cache to file", zap.Error(err), zap.String("cache", name))
		}
	}

	duration := time.Since(start).Milliseconds()
	durationString := strconv.FormatInt(duration, 10) + "ms"
	logger.Info("Persisted caches", zap.String("duration", durationString))
}

func logCacheStats(goCaches map[string]*gocache.Cache, logger *zap.Logger) {
	for name, goCache := range goCaches {
		logger.Info("Cache stats", zap.String("cache", name), zap.Int("itemCount", goCache.ItemCount()))
	}
}
