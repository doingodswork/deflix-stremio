package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v2"
	"github.com/go-redis/redis/v8"
	"github.com/markbates/pkger"
	gocache "github.com/patrickmn/go-cache"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/deflix-tv/go-stremio"
	"github.com/deflix-tv/go-stremio/pkg/cinemeta"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/alldebrid"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/realdebrid"
	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
	"github.com/doingodswork/deflix-stremio/pkg/logadapter"
)

const (
	version = "0.9.1"
)

var manifest = stremio.Manifest{
	ID:          "tv.deflix.stremio",
	Name:        "Deflix - Debrid flicks",
	Description: "Finds movies on YTS, The Pirate Bay, 1337x, RARBG and ibit and automatically turns them into cached HTTP streams with a debrid service like RealDebrid or AllDebrid, for high speed 4k streaming and no P2P uploading (!). For more info see https://www.deflix.tv",
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

	BehaviorHints: stremio.BehaviorHints{
		P2P:                   false,
		Configurable:          true,
		ConfigurationRequired: true,
	},
}

var (
	// Timeout used for HTTP requests in the cinemeta, imdb2torrent and realdebrid clients.
	timeout = 5 * time.Second
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

// Persistent stores
var (
	// BadgerDB
	torrentCache  *resultStore
	cinemetaCache *metaStore
)

// In-memory caches, filled from a file on startup and persisted to a file in regular intervals.
var (
	// go-cache
	rdAvailabilityCache *creationCache
	adAvailabilityCache *creationCache
	tokenCache          *creationCache
	// go-cache or Redis, depending on config
	redirectCache *goCache
	streamCache   *goCache
)

// Clients
var (
	cinemetaClient *cinemeta.Client
	searchClient   *imdb2torrent.Client
	rdClient       *realdebrid.Client
	adClient       *alldebrid.Client
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

	if config.StoragePath == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			logger.Fatal("Couldn't determine user cache directory via `os.UserCacheDir()`", zap.Error(err))
		}
		// Add two levels, because even if we're in `os.UserCacheDir()`, on Windows that's for example `C:\Users\John\AppData\Local`
		config.StoragePath = filepath.Join(userCacheDir, "deflix-stremio/badger")
	} else {
		config.StoragePath = filepath.Clean(config.StoragePath)
	}
	// If the dir doesn't exist, BadgerDB creates it when writing its DB files.

	if config.CachePath == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			logger.Fatal("Couldn't determine user cache directory via `os.UserCacheDir()`", zap.Error(err))
		}
		// Add two levels, because even if we're in `os.UserCacheDir()`, on Windows that's for example `C:\Users\John\AppData\Local`
		config.CachePath = filepath.Join(userCacheDir, "deflix-stremio/cache")
	} else {
		config.CachePath = filepath.Clean(config.CachePath)
	}
	// If the dir doesn't exist, it's created when the files are written.

	// Load or create caches and stores

	// Caches first, because some things can go wrong here, and we don't have the store closer yet, which can lead to corrupted BadgerDB files.
	initCaches(config, logger)

	closer := initStores(config, logger)
	defer func() {
		if err := closer(); err != nil {
			logger.Error("Couldn't close all stores", zap.Error(err))
		}
	}()

	// Create clients

	initClients(config, logger)

	// Init cache maps

	goCaches := map[string]*gocache.Cache{
		"availability-ad": rdAvailabilityCache.cache,
		"availability-rd": adAvailabilityCache.cache,
		"token":           tokenCache.cache,
	}
	if redirectCache.cache != nil {
		goCaches["redirect"] = redirectCache.cache
	}
	if streamCache.cache != nil {
		goCaches["stream"] = streamCache.cache
	}
	// Log cache stats every hour
	go func() {
		// Don't run at the same time as the persistence
		time.Sleep(time.Minute)
		for {
			logCacheStats(goCaches, logger)
			time.Sleep(time.Hour)
		}
	}()

	// Prepare addon creation

	streamHandler := createStreamHandler(config, searchClient, rdClient, adClient, redirectCache, logger)
	streamHandlers := map[string]stremio.StreamHandler{"movie": streamHandler}

	var httpFS http.FileSystem
	if config.WebConfigurePath == "" {
		httpFS = pkger.Dir("/web/configure")
	} else {
		configurePath := filepath.Clean(config.WebConfigurePath)
		logger.Info("Cleaned web configure path", zap.String("path", configurePath))
		httpFS = http.Dir(configurePath)
	}
	options := stremio.Options{
		BindAddr: config.BindAddr,
		Port:     config.Port,
		// We already have a logger
		Logger:       logger,
		LogIPs:       true,
		RedirectURL:  config.RootURL,
		LogMediaName: true,
		// We already have a Cinemeta Client
		CinemetaClient:  cinemetaClient,
		ConfigureHTMLfs: httpFS,
		StreamIDregex:   "tt\\d{7,8}",
	}

	// Create addon

	addon, err := stremio.NewAddon(manifest, nil, streamHandlers, options)
	if err != nil {
		logger.Fatal("Couldn't create new addon", zap.Error(err))
	}

	// Customize addon

	tokenMiddleware := createTokenMiddleware(rdClient, adClient, logger)
	addon.AddMiddleware("/:userData/manifest.json", tokenMiddleware)
	addon.AddMiddleware("/:userData/stream/:type/:id.json", tokenMiddleware)
	// No need to set the middleware to the stream route without user data because go-stremio blocks it (with a 400 Bad Request response) if BehaviorHints.ConfigurationRequired is true.

	// Requires URL query: "?imdbid=123&apitoken=foo"
	statusEndpoint := createStatusHandler(searchClient.GetMagnetSearchers(), rdClient, adClient, goCaches, logger)
	addon.AddEndpoint("GET", "/status", statusEndpoint)

	// Redirects stream URLs (previously sent to Stremio) to the actual RealDebrid stream URLs
	addon.AddEndpoint("GET", "/redirect/:id", createRedirectHandler(redirectCache, streamCache, rdClient, adClient, logger))

	// Save cache to file every hour
	go func() {
		for {
			time.Sleep(time.Hour)
			persistCaches(ctx, config.CachePath, goCaches, logger)
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

func initStores(config config, logger *zap.Logger) (closer func() error) {
	logger.Info("Initializing stores...")
	start := time.Now()

	var closers []func() error
	multiCloser := func() error {
		var result error
		for _, closer := range closers {
			if err := closer(); err != nil {
				multierr.Append(result, err)
			}
		}
		return result
	}

	// BadgerDB
	options := badger.DefaultOptions(config.StoragePath)
	options.SyncWrites = false
	options.Logger = logadapter.NewBadger2Zap(logger)
	db, err := badger.Open(options)
	if err != nil {
		logger.Fatal("Couldn't open BadgerDB", zap.Error(err))
	}
	closers = append(closers, db.Close)

	torrentCache = &resultStore{
		db:        db,
		keyPrefix: "torrent_",
	}
	cinemetaCache = &metaStore{
		db:        db,
		keyPrefix: "meta_",
	}

	// Periodically call RunValueLogGC()
	go func() {
		time.Sleep(time.Hour)
		for {
			db.RunValueLogGC(0.5)
			time.Sleep(time.Hour)
		}
	}()

	duration := time.Since(start).Milliseconds()
	durationString := strconv.FormatInt(duration, 10) + "ms"
	logger.Info("Initialized stores", zap.String("duration", durationString))

	return multiCloser
}

func initCaches(config config, logger *zap.Logger) {
	logger.Info("Initializing caches...")
	start := time.Now()

	rdAvailabilityCacheItems, err := loadGoCache(config.CachePath + "/availability-rd.gob")
	if err != nil {
		logger.Error("Couldn't load RD availability cache from file - continuing with an empty cache", zap.Error(err))
		rdAvailabilityCacheItems = map[string]gocache.Item{}
	}
	rdAvailabilityCache = &creationCache{
		cache: gocache.NewFrom(config.CacheAgeXD, 24*time.Hour, rdAvailabilityCacheItems),
	}

	adAvailabilityCacheItems, err := loadGoCache(config.CachePath + "/availability-ad.gob")
	if err != nil {
		logger.Error("Couldn't load AD availability cache from file - continuing with an empty cache", zap.Error(err))
		adAvailabilityCacheItems = map[string]gocache.Item{}
	}
	adAvailabilityCache = &creationCache{
		cache: gocache.NewFrom(config.CacheAgeXD, 24*time.Hour, adAvailabilityCacheItems),
	}

	// TODO: Return closer func like in the stores initialization function.
	var rdb *redis.Client
	if config.RedisAddr != "" {
		redisOpts := redis.Options{
			Addr: config.RedisAddr,
		}
		if config.RedisCreds != "" {
			if strings.Contains(config.RedisCreds, ":") {
				creds := strings.SplitN(config.RedisCreds, ":", 2)
				redisOpts.Username = creds[0]
				redisOpts.Password = creds[1]
			} else {
				redisOpts.Password = config.RedisCreds
			}
		}
		rdb = redis.NewClient(&redisOpts)
		logger.Info("Testing connection to Redis...")
		if err := rdb.Ping(context.Background()).Err(); err != nil {
			logger.Fatal("Couldn't ping Redis", zap.Error(err))
		}
		logger.Info("Connection to Redis established!")
	}

	if config.RedisAddr == "" {
		if redirectCacheItems, err := loadGoCache(config.CachePath + "/redirect.gob"); err != nil {
			logger.Error("Couldn't load redirect cache from file - continuing with an empty cache", zap.Error(err))
			redirectCache = &goCache{
				cache: gocache.New(redirectExpiration, 24*time.Hour),
			}
		} else {
			redirectCache = &goCache{
				cache: gocache.NewFrom(redirectExpiration, 24*time.Hour, redirectCacheItems),
			}
		}
	} else {
		var t []imdb2torrent.Result
		redirectCache = &goCache{
			rdb:    rdb,
			t:      reflect.TypeOf(t),
			logger: logger,
		}
	}

	if config.RedisAddr == "" {
		if streamCacheItems, err := loadGoCache(config.CachePath + "/stream.gob"); err != nil {
			logger.Error("Couldn't load stream cache from file - continuing with an empty cache", zap.Error(err))
			streamCache = &goCache{
				cache: gocache.New(streamExpiration, 24*time.Hour),
			}
		} else {
			streamCache = &goCache{
				cache: gocache.NewFrom(streamExpiration, 24*time.Hour, streamCacheItems),
			}
		}
	} else {
		var t cacheItem
		streamCache = &goCache{
			rdb:    rdb,
			t:      reflect.TypeOf(t),
			logger: logger,
		}
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
	logger.Info("Initialized caches", zap.String("duration", durationString))
}

func initClients(config config, logger *zap.Logger) {
	logger.Info("Initializing clients...")
	start := time.Now()

	ytsClientOpts := imdb2torrent.NewYTSclientOpts(config.BaseURLyts, timeout, config.MaxAgeTorrents)
	tpbClientOpts := imdb2torrent.NewTPBclientOpts(config.BaseURLtpb, config.SocksProxyAddrTPB, timeout, config.MaxAgeTorrents)
	leetxClientOpts := imdb2torrent.NewLeetxClientOpts(config.BaseURL1337x, timeout, config.MaxAgeTorrents)
	ibitClientOpts := imdb2torrent.NewIbitClientOpts(config.BaseURLibit, timeout, config.MaxAgeTorrents)
	rarbgClientOpts := imdb2torrent.NewRARBGclientOpts(config.BaseURLrarbg, timeout, config.MaxAgeTorrents)
	rdClientOpts := realdebrid.NewClientOpts(config.BaseURLrd, timeout, config.CacheAgeXD, config.ExtraHeadersXD)
	adClientOpts := alldebrid.NewClientOpts(config.BaseURLad, timeout, config.CacheAgeXD, config.ExtraHeadersXD)

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
		"RARBG": imdb2torrent.NewRARBGclient(rarbgClientOpts, torrentCache, logger, config.LogFoundTorrents),
	}
	searchClient = imdb2torrent.NewClient(siteClients, timeout, logger)
	rdClient, err = realdebrid.NewClient(rdClientOpts, tokenCache, rdAvailabilityCache, logger)
	if err != nil {
		logger.Fatal("Couldn't create RealDebrid client", zap.Error(err))
	}
	adClient, err = alldebrid.NewClient(adClientOpts, tokenCache, adAvailabilityCache, logger)
	if err != nil {
		logger.Fatal("Couldn't create AllDebrid client", zap.Error(err))
	}

	duration := time.Since(start).Milliseconds()
	durationString := strconv.FormatInt(duration, 10) + "ms"
	logger.Info("Initialized clients", zap.String("duration", durationString))
}
