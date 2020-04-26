package main

import (
	"os"
	"testing"
	"time"

	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
	"github.com/google/go-cmp/cmp"
	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

func TestGoCacheItem(t *testing.T) {
	cache := gocache.New(0, 0)
	exp := cacheItem{
		Value:   "foo",
		Created: time.Now(),
	}
	cache.Set("123", exp, 0)
	actualIface, found := cache.Get("123")
	require.True(t, found)
	actual, ok := actualIface.(cacheItem)
	require.True(t, ok)
	require.Equal(t, exp, actual)
}

func TestGoCachePersistence(t *testing.T) {
	registerTypes()

	cache := gocache.New(0, 0)
	exp1 := cacheItem{
		Value:   "foo",
		Created: time.Now(),
	}
	exp2 := []imdb2torrent.Result{
		{Title: "Big Buck Bunny"},
		{Title: "Sintel"},
	}
	cache.Set("123", exp1, 0)
	cache.Set("456", exp2, 0)
	filePath := os.TempDir() + ".gocache"
	err := saveGoCache(cache.Items(), filePath)
	require.NoError(t, err)

	items, err := loadGoCache(filePath)
	require.NoError(t, err)
	cache = gocache.NewFrom(0, 0, items)

	actualIface, found := cache.Get("123")
	require.True(t, found)
	actual1, ok := actualIface.(cacheItem)
	require.True(t, ok)
	// We can't use require.Equal here, because the marshalled time loses its wall time, leading to a difference for the internally used reflect.DeepEquals.
	equal := cmp.Equal(exp1, actual1)
	require.True(t, equal)

	actualIface, found = cache.Get("456")
	require.True(t, found)
	actual2, ok := actualIface.([]imdb2torrent.Result)
	require.True(t, ok)
	// We can't use require.Equal here, because the marshalled time loses its wall time, leading to a difference for the internally used reflect.DeepEquals.
	equal = cmp.Equal(exp2, actual2)
	require.True(t, equal)
}
