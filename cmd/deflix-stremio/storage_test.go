package main

import (
	"math"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/go-cmp/cmp"
	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"

	"github.com/deflix-tv/go-stremio"
	"github.com/deflix-tv/imdb2torrent"
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

func TestRedis(t *testing.T) {
	// Doesn't work on Windows: https://github.com/testcontainers/testcontainers-go/issues/152
	// ip, port, deferFunc := startRedis(t)
	// defer deferFunc()
	ip, port := "localhost", "6379"

	logger, err := stremio.NewLogger("debug", "")
	require.NoError(t, err)

	// Type: torrent result slice (for redirect cache use case)

	var type1 []imdb2torrent.Result
	gc := goCache{
		rdb: redis.NewClient(&redis.Options{
			Addr: ip + ":" + port,
		}),
		t:      reflect.TypeOf(type1),
		logger: logger,
	}
	k := strconv.Itoa(rand.Intn(math.MaxUint32))
	// Empty Get
	_, found := gc.Get(k)
	require.False(t, found)
	// Set
	v1 := []imdb2torrent.Result{
		{
			InfoHash:  "123",
			MagnetURL: "magnet:?xt=urn:btih:123",
			Title:     "foo",
			Quality:   "720p",
		},
		{
			InfoHash:  "456",
			MagnetURL: "magnet:?xt=urn:btih:456",
			Title:     "foo",
			Quality:   "720p",
		},
	}
	gc.Set(k, v1, time.Minute)
	// Get
	res, found := gc.Get(k)
	require.True(t, found)
	require.Equal(t, v1, res)

	// Type: cacheItem (for stream cache use case)

	var type2 cacheItem
	gc.t = reflect.TypeOf(type2)
	k = strconv.Itoa(rand.Intn(math.MaxUint32))
	// Empty Get
	_, found = gc.Get(k)
	require.False(t, found)
	// Set
	v2 := cacheItem{
		Value:   "foo",
		Created: time.Now().Truncate(0), // Truncate to strip monotonic clock, which doesn't get included when encoding/decoding
	}
	gc.Set(k, v2, time.Minute)
	// Get
	res, found = gc.Get(k)
	require.True(t, found)
	require.Equal(t, v2, res)
}

// Doesn't work on Windows in v0.9.0: https://github.com/testcontainers/testcontainers-go/issues/152
// We need to comment out the function to not have the dependency in the go.mod, which leads to compile errors due to the linked bug.
// func startRedis(t *testing.T) (string, string, func()) {
// 	ctx := context.Background()
// 	p, err := nat.NewPort("tcp", "6379")
// 	require.NoError(t, err)
// 	req := testcontainers.ContainerRequest{
// 		Image:        "redis:6-alpine",
// 		ExposedPorts: []string{"6379/tcp"},
// 		WaitingFor:   wait.ForListeningPort(p),
// 	}
// 	redisC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
// 		ContainerRequest: req,
// 		Started:          true,
// 	})
// 	require.NoError(t, err)
// 	ip, err := redisC.Host(ctx)
// 	if err != nil {
// 		redisC.Terminate(ctx)
// 		require.NoError(t, err)
// 	}
// 	port, err := redisC.MappedPort(ctx, "6379")
// 	if err != nil {
// 		redisC.Terminate(ctx)
// 		require.NoError(t, err)
// 	}

// 	return ip, port.Port(), func() { redisC.Terminate(ctx) }
// }
