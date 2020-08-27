package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	gocache "github.com/patrickmn/go-cache"
	"go.uber.org/zap"

	"github.com/deflix-tv/go-stremio"
	"github.com/deflix-tv/go-stremio/pkg/cinemeta"
	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
	"github.com/doingodswork/deflix-stremio/pkg/realdebrid"
)

const (
	version = "0.8.1"
)

var manifest = stremio.Manifest{
	ID:          "tv.deflix.stremio",
	Name:        "Deflix - Debrid flicks",
	Description: "Finds movies on YTS, The Pirate Bay, 1337x and ibit and automatically turns your selected torrent into a cached HTTP stream from a debrid provider like RealDebrid, for  high speed 4k streaming and no P2P uploading (!). For more info see https://www.deflix.tv",
	Version:     version,

	ResourceItems: []stremio.ResourceItem{
		{
			Name:  "stream",
			Types: []string{"movie"},
			// Not required as long as we define them globally in the manifest
			//IDprefixes: []string{"tt"},
		},
	},
	Types: []string{"movie"},
	// An empty slice is required for serializing to a JSON that Stremio expects
	Catalogs: []stremio.CatalogItem{},

	IDprefixes: []string{"tt"},
	// Must use www.deflix.tv instead of just deflix.tv because GitHub takes care of redirecting non-www to www and this leads to HTTPS certificate issues.
	Background: "https://www.deflix.tv/images/Logo-1024px.png",
	Logo:       "https://www.deflix.tv/images/Logo-250px.png",
}

var (
	// Timeout used for HTTP requests in the cinemeta, imdb2torrent and realdebrid clients.
	timeout = 5 * time.Second
	// Expiration for cached cinemeta.Meta objects. They rarely (if ever) change, so make it 1 month.
	cinemetaExpiration = 30 * 24 * time.Hour
	// Expiration for the data that's passed from the stream handler to the redirect handler.
	// 24h so that a user who selects a movie and sees the list of streams can click on a stream within this time.
	// If a user stops/exits a stream and later resumes it, Stremio sends him to the redirect handler. If the stream cache doesn't hold the cache anymore, we just get fresh torrents - no need to cache this for so long.
	redirectExpiration = 24 * time.Hour
	// Expiration for the converted stream inside the stream handler.
	// A long expiration is important for a user who stops/exits a stream and later resumes it. Stremio sends him to the redirect handler.
	// 10 days: weekend -> next weekend.
	// TODO: We don't know how long an RealDebrid stream URL is valid - so maybe this should be shorter (returning an invalid stream URL is worse then doing another torrent lookup + RealDebrid conversion, but keep in mind that the video player might have issues when another URL of the same file, or a completely other file (for example because the previous one isn't available on RealDebrid anymore) is returned). Also see similar TODO comment in handlers.go file.
	streamExpiration = 10 * 24 * time.Hour // 10 days
	// Expiration for cached users' RealDebrid API tokens
	tokenExpiration = 24 * time.Hour
)

// In-memory caches, filled from a file on startup and persisted to a file in regular intervals.
// Use different cache instances so that for example a high churn (new entries pushing out old ones) in the torrent cache doesn't lead to entries in other caches being lost.
// Also use different cache types - fastcache seems to be inefficient for small values (600 items with a short string and time leads to 32 MB) for example, while go-cache can't be limited in size. So we use fastcache for caches that could grow really big, and go-cache for caches where we know it'll stay small, or were we purge old entries regularly.
var (
	// fastcache
	torrentCache *resultCache
	// go-cache
	availabilityCache *creationCache
	cinemetaCache     *metaCache
	redirectCache     *gocache.Cache
	streamCache       *gocache.Cache
	tokenCache        *creationCache
)

// Clients
var (
	cinemetaClient   *cinemeta.Client
	searchClient     *imdb2torrent.Client
	conversionClient *realdebrid.Client
)

var (
	// Locks the redirectLock map
	redirectLockMapLock = sync.Mutex{}
	// Locks redirect handler cache lookup/write and execution per redirectID
	redirectLock = map[string]*sync.Mutex{}
)

func init() {
	// Timeout for global default HTTP client (for when using `http.Get()`)
	http.DefaultClient.Timeout = 5 * time.Second

	// Make predicting "random" numbers harder
	rand.NewSource(time.Now().UnixNano())

	// Register types for gob en- and decoding, required when using go-cache, because a go-cache item is always an `interface{}`.
	registerTypes()
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	// Create an "info" logger at first, replace later in case the logging level is configured to be something else
	logger, err := stremio.NewLogger("info")
	if err != nil {
		panic(err)
	}

	// Parse config

	logger.Info("Parsing config...")
	config := parseConfig(logger)
	configJSON, err := json.Marshal(config)
	if err != nil {
		logger.Fatal("Couldn't marshal config to JSON", zap.Error(err))
	}

	if config.LogLevel != "info" {
		// Replace previously created logger
		if logger, err = stremio.NewLogger(config.LogLevel); err != nil {
			logger.Fatal("Couldn't create new logger", zap.Error(err))
		}
	}

	logger.Info("Parsed config", zap.ByteString("config", configJSON))

	if config.CachePath == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			logger.Fatal("Couldn't determine user cache directory via `os.UserCacheDir()`", zap.Error(err))
		}
		// Add two levels, because even if we're in `os.UserCacheDir()`, on Windows that's for example `C:\Users\John\AppData\Local`
		config.CachePath = userCacheDir + "/deflix-stremio/cache"
	} else {
		config.CachePath = strings.TrimSuffix(config.CachePath, "/")
	}

	// Load or create caches

	initCaches(config, logger)

	// Create clients

	initClients(config, logger)

	// Init cache maps

	fastCaches := map[string]*fastcache.Cache{
		"torrent": torrentCache.cache,
	}
	goCaches := map[string]*gocache.Cache{
		"availability": availabilityCache.cache,
		"cinemeta":     cinemetaCache.cache,
		"redirect":     redirectCache,
		"stream":       streamCache,
		"token":        tokenCache.cache,
	}
	// Log cache stats every hour
	go func() {
		// Don't run at the same time as the persistence
		time.Sleep(time.Minute)
		for {
			logCacheStats(fastCaches, goCaches, logger)
			time.Sleep(time.Hour)
		}
	}()

	// Prepare addon creation

	streamHandler := createStreamHandler(config, searchClient, conversionClient, redirectCache, logger)
	streamHandlers := map[string]stremio.StreamHandler{"movie": streamHandler}

	options := stremio.Options{
		BindAddr: config.BindAddr,
		Port:     config.Port,
		// We already have a logger
		Logger:       logger,
		LogIPs:       true,
		RedirectURL:  config.RootURL,
		LogMediaName: true,
		// We already have a Cinemeta Client
		CinemetaClient: cinemetaClient,
	}

	// Create addon

	addon, err := stremio.NewAddon(manifest, nil, streamHandlers, options)
	if err != nil {
		logger.Fatal("Couldn't create new addon", zap.Error(err))
	}

	// Customize addon

	tokenMiddleware := createTokenMiddleware(conversionClient, logger)
	addon.AddMiddleware("/:userData/manifest.json", tokenMiddleware)
	addon.AddMiddleware("/:userData/stream/:type/:id.json", tokenMiddleware)
	// Also set the middleware for the endpoints without userData, so that in the handlers we don't have to deal with the possibility that the token isn't set.
	addon.AddMiddleware("/manifest.json", tokenMiddleware)
	addon.AddMiddleware("/stream/:type/:id.json", tokenMiddleware)

	// Requires URL query: "?imdbid=123&apitoken=foo"
	statusEndpoint := createStatusHandler(searchClient.GetMagnetSearchers(), conversionClient, fastCaches, goCaches, logger)
	addon.AddEndpoint("GET", "/status", statusEndpoint)

	// Redirects stream URLs (previously sent to Stremio) to the actual RealDebrid stream URLs
	addon.AddEndpoint("GET", "/redirect/:id", createRedirectHandler(redirectCache, conversionClient, logger))

	// Save cache to file every hour
	go func() {
		for {
			time.Sleep(time.Hour)
			persistCaches(ctx, config.CachePath, fastCaches, goCaches, logger)
		}
	}()

	// Start addon

	stoppingChan := make(chan bool, 1)
	go func() {
		<-stoppingChan
		cancel()
	}()

	addon.Run(stoppingChan)
}

func initCaches(config config, logger *zap.Logger) {
	logger.Info("Initiating caches...")
	start := time.Now()

	// fastcache
	cacheMaxBytes := config.CacheMaxMB * 1000 * 1000
	torrentCache = &resultCache{
		cache: fastcache.LoadFromFileOrNew(config.CachePath+"/torrent", cacheMaxBytes),
	}

	// go-caches

	availabilityCacheItems, err := loadGoCache(config.CachePath + "/availability.gob")
	if err != nil {
		logger.Error("Couldn't load availability cache from file - continuing with an empty cache", zap.Error(err))
		availabilityCacheItems = map[string]gocache.Item{}
	}
	availabilityCache = &creationCache{
		cache: gocache.NewFrom(config.CacheAgeRD, 24*time.Hour, availabilityCacheItems),
	}

	cinemetaCacheItems, err := loadGoCache(config.CachePath + "/cinemeta.gob")
	if err != nil {
		logger.Error("Couldn't load cinemeta cache from file - continuing with an empty cache", zap.Error(err))
		cinemetaCacheItems = map[string]gocache.Item{}
	}
	cinemetaCache = &metaCache{
		cache: gocache.NewFrom(cinemetaExpiration, 24*time.Hour, cinemetaCacheItems),
	}

	if redirectCacheItems, err := loadGoCache(config.CachePath + "/redirect.gob"); err != nil {
		logger.Error("Couldn't load redirect cache from file - continuing with an empty cache", zap.Error(err))
		redirectCache = gocache.New(redirectExpiration, 24*time.Hour)
	} else {
		redirectCache = gocache.NewFrom(redirectExpiration, 24*time.Hour, redirectCacheItems)
	}

	if streamCacheItems, err := loadGoCache(config.CachePath + "/stream.gob"); err != nil {
		logger.Error("Couldn't load stream cache from file - continuing with an empty cache", zap.Error(err))
		streamCache = gocache.New(streamExpiration, 24*time.Hour)
	} else {
		streamCache = gocache.NewFrom(streamExpiration, 24*time.Hour, streamCacheItems)
	}

	tokenCacheItems, err := loadGoCache(config.CachePath + "/token.gob")
	if err != nil {
		logger.Error("Couldn't load token cache from file - continuing with an empty cache", zap.Error(err))
		tokenCacheItems = map[string]gocache.Item{}
	}
	tokenCache = &creationCache{
		cache: gocache.NewFrom(tokenExpiration, 24*time.Hour, tokenCacheItems),
	}

	duration := time.Since(start).Milliseconds()
	durationString := strconv.FormatInt(duration, 10) + "ms"
	logger.Info("Initiated caches", zap.String("duration", durationString))
}

func initClients(config config, logger *zap.Logger) {
	logger.Info("Initiating clients...")
	start := time.Now()

	ytsClientOpts := imdb2torrent.NewYTSclientOpts(config.BaseURLyts, timeout, config.CacheAgeTorrents)
	tpbClientOpts := imdb2torrent.NewTPBclientOpts(config.BaseURLtpb, config.SocksProxyAddrTPB, timeout, config.CacheAgeTorrents)
	leetxClientOpts := imdb2torrent.NewLeetxClientOpts(config.BaseURL1337x, timeout, config.CacheAgeTorrents)
	ibitClientOpts := imdb2torrent.NewIbitClientOpts(config.BaseURLibit, timeout, config.CacheAgeTorrents)
	rdClientOpts := realdebrid.NewClientOpts(config.BaseURLrd, timeout, config.CacheAgeRD, config.ExtraHeadersRD)

	cinemetaClient = cinemeta.NewClient(cinemeta.DefaultClientOpts, cinemetaCache, logger)
	tpbClient, err := imdb2torrent.NewTPBclient(tpbClientOpts, torrentCache, cinemetaClient, logger, config.LogFoundTorrents)
	if err != nil {
		logger.Fatal("Couldn't create TPB client", zap.Error(err))
	}
	siteClients := map[string]imdb2torrent.MagnetSearcher{
		"YTS":   imdb2torrent.NewYTSclient(ytsClientOpts, torrentCache, logger, config.LogFoundTorrents),
		"TPB":   tpbClient,
		"1337X": imdb2torrent.NewLeetxClient(leetxClientOpts, torrentCache, cinemetaClient, logger, config.LogFoundTorrents),
		"ibit":  imdb2torrent.NewIbitClient(ibitClientOpts, torrentCache, logger, config.LogFoundTorrents),
	}
	searchClient = imdb2torrent.NewClient(siteClients, timeout, logger)
	conversionClient, err = realdebrid.NewClient(rdClientOpts, tokenCache, availabilityCache, logger)
	if err != nil {
		logger.Fatal("Couldn't create RealDebrid client", zap.Error(err))
	}

	duration := time.Since(start).Milliseconds()
	durationString := strconv.FormatInt(duration, 10) + "ms"
	logger.Info("Initiated clients", zap.String("duration", durationString))
}
