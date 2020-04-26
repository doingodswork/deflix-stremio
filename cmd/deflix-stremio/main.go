package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	gocache "github.com/patrickmn/go-cache"
	log "github.com/sirupsen/logrus"

	"github.com/doingodswork/deflix-stremio/pkg/cinemata"
	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
	"github.com/doingodswork/deflix-stremio/pkg/realdebrid"
	"github.com/doingodswork/deflix-stremio/pkg/stremio"
)

const (
	version = "0.6.0"
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
	// Timeout used for HTTP requests in the cinemata, imdb2torrent and realdebrid clients.
	timeout = 5 * time.Second
	// Expiration for cached cinemata.Movie objects. They rarely (if ever) change, so make it 1 month.
	cinemataExpiration = 30 * 24 * time.Hour
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
	torrentCache resultCache
	// go-cache
	availabilityCache creationCache
	cinemataCache     movieCache
	redirectCache     *gocache.Cache
	streamCache       *gocache.Cache
	tokenCache        creationCache
)

// Clients
var (
	cinemataClient   cinemata.Client
	searchClient     imdb2torrent.Client
	conversionClient realdebrid.Client
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

	// Configure logging (except for level, which we only know from the config which is obtained later).
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	// Register types for gob en- and decoding, required when using go-cache, because a go-cache item is always an `interface{}`.
	registerTypes()
}

func main() {
	mainCtx := context.Background()

	// Parse config

	log.Info("Parsing config...")
	config := parseConfig(mainCtx)
	configJSON, err := json.Marshal(config)
	if err != nil {
		log.WithError(err).Fatal("Couldn't marshal config to JSON")
	}

	setLogLevel(config)

	log.WithField("config", string(configJSON)).Info("Parsed config")

	if config.CachePath == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			log.WithError(err).Fatal("Couldn't determine user cache directory via `os.UserCacheDir()`")
		}
		// Add two levels, because even if we're in `os.UserCacheDir()`, on Windows that's for example `C:\Users\John\AppData\Local`
		config.CachePath = userCacheDir + "/deflix-stremio/cache"
	} else {
		config.CachePath = strings.TrimSuffix(config.CachePath, "/")
	}

	// Load or create caches

	initCaches(mainCtx, config)

	// Create clients

	initClients(mainCtx, config)

	// Basic middleware and health endpoint

	log.Info("Setting up server")
	r := mux.NewRouter()
	s := r.Methods("GET").Subrouter()
	s.Use(createTimerMiddleware(mainCtx),
		createCorsMiddleware(mainCtx), // Stremio doesn't show stream responses when no CORS middleware is used!
		handlers.ProxyHeaders,
		recoveryMiddleware,
		createLoggingMiddleware(mainCtx, cinemataCache))
	s.HandleFunc("/health", healthHandler)
	fastCaches := map[string]*fastcache.Cache{
		"torrent": torrentCache.cache,
	}
	goCaches := map[string]*gocache.Cache{
		"availability": availabilityCache.cache,
		"cinemata":     cinemataCache.cache,
		"redirect":     redirectCache,
		"stream":       streamCache,
		"token":        tokenCache.cache,
	}
	// Requires URL query: "?imdbid=123&apitoken=foo"
	s.HandleFunc("/status", createStatusHandler(mainCtx, searchClient.GetMagnetSearchers(), conversionClient, fastCaches, goCaches))

	// Stremio endpoints

	// Use token middleware only for the Stremio endpoints
	tokenMiddleware := createTokenMiddleware(mainCtx, conversionClient)
	manifestHandler := createManifestHandler(mainCtx, conversionClient)
	streamHandler := createStreamHandler(mainCtx, config, searchClient, conversionClient, redirectCache)
	s.HandleFunc("/{apitoken}/manifest.json", tokenMiddleware(manifestHandler).ServeHTTP)
	s.HandleFunc("/{apitoken}/stream/{type}/{id}.json", tokenMiddleware(streamHandler).ServeHTTP)

	// Additional endpoints

	// Redirects stream URLs (previously sent to Stremio) to the actual RealDebrid stream URLs
	s.HandleFunc("/redirect/{id}", createRedirectHandler(mainCtx, redirectCache, conversionClient))
	// Root redirects to website
	s.HandleFunc("/", createRootHandler(mainCtx, config))

	srv := &http.Server{
		Addr:    config.BindAddr + ":" + strconv.Itoa(config.Port),
		Handler: s,
		// Timeouts to avoid Slowloris attacks
		ReadTimeout:    time.Second * 5,
		WriteTimeout:   time.Second * 15,
		IdleTimeout:    time.Second * 60,
		MaxHeaderBytes: 1 * 1000, // 1 KB
	}

	stopping := false
	stoppingPtr := &stopping

	log.WithField("address", srv.Addr).Info("Starting server")
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			if !*stoppingPtr {
				log.WithError(err).Fatal("Couldn't start server")
			} else {
				log.WithError(err).Fatal("Error in srv.ListenAndServe() during server shutdown (probably context deadline expired before the server could shutdown cleanly)")
			}
		}
	}()

	// Timed logger for easier debugging with logs
	go func() {
		for {
			log.Trace("...")
			time.Sleep(time.Second)
		}
	}()

	// Save cache to file every hour
	go func() {
		for {
			time.Sleep(time.Hour)
			persistCaches(mainCtx, config.CachePath, stoppingPtr, fastCaches, goCaches)
		}
	}()

	// Log cache stats every hour
	go func() {
		// Don't run at the same time as the persistence
		time.Sleep(time.Minute)
		for {
			logCacheStats(mainCtx, fastCaches, goCaches)
			time.Sleep(time.Hour)
		}
	}()

	// Graceful shutdown

	c := make(chan os.Signal, 1)
	// Accept SIGINT (Ctrl+C) and SIGTERM (`docker stop`)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	sig := <-c
	log.WithField("signal", sig).Info("Received signal, shutting down...")
	*stoppingPtr = true
	// Create a deadline to wait for. `docker stop` gives us 10 seconds.
	// No need to get the cancel func and defer calling it, because srv.Shutdown() will consider the timeout from the context.
	ctx, _ := context.WithTimeout(context.Background(), 9*time.Second)
	// Doesn't block if no connections, but will otherwise wait until the timeout deadline
	if err := srv.Shutdown(ctx); err != nil {
		log.WithError(err).Fatal("Error shutting down server")
	}
	log.Info("Server shut down")
}

func setLogLevel(cfg config) {
	switch cfg.LogLevel {
	case "trace":
		log.SetLevel(log.TraceLevel)
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	case "fatal":
		log.SetLevel(log.FatalLevel)
	case "panic":
		log.SetLevel(log.PanicLevel)
	default:
		log.WithField("logLevel", cfg.LogLevel).Fatal("Unknown logLevel")
	}
}

func initCaches(ctx context.Context, config config) {
	log.Info("Initiating caches...")
	start := time.Now()

	// fastcache
	cacheMaxBytes := config.CacheMaxMB * 1000 * 1000
	torrentCache = resultCache{
		cache: fastcache.LoadFromFileOrNew(config.CachePath+"/torrent", cacheMaxBytes),
	}

	// go-caches

	availabilityCacheItems, err := loadGoCache(config.CachePath + "/availability.gob")
	if err != nil {
		log.WithError(err).Error("Couldn't load availability cache from file - continuing with an empty cache")
		availabilityCacheItems = map[string]gocache.Item{}
	}
	availabilityCache = creationCache{
		cache: gocache.NewFrom(config.CacheAgeRD, 24*time.Hour, availabilityCacheItems),
	}

	cinemataCacheItems, err := loadGoCache(config.CachePath + "/cinemata.gob")
	if err != nil {
		log.WithError(err).Error("Couldn't load cinemata cache from file - continuing with an empty cache")
		cinemataCacheItems = map[string]gocache.Item{}
	}
	cinemataCache = movieCache{
		cache: gocache.NewFrom(cinemataExpiration, 24*time.Hour, cinemataCacheItems),
	}

	if redirectCacheItems, err := loadGoCache(config.CachePath + "/redirect.gob"); err != nil {
		log.WithError(err).Error("Couldn't load redirect cache from file - continuing with an empty cache")
		redirectCache = gocache.New(redirectExpiration, 24*time.Hour)
	} else {
		redirectCache = gocache.NewFrom(redirectExpiration, 24*time.Hour, redirectCacheItems)
	}

	if streamCacheItems, err := loadGoCache(config.CachePath + "/stream.gob"); err != nil {
		log.WithError(err).Error("Couldn't load stream cache from file - continuing with an empty cache")
		streamCache = gocache.New(streamExpiration, 24*time.Hour)
	} else {
		streamCache = gocache.NewFrom(streamExpiration, 24*time.Hour, streamCacheItems)
	}

	tokenCacheItems, err := loadGoCache(config.CachePath + "/token.gob")
	if err != nil {
		log.WithError(err).Error("Couldn't load token cache from file - continuing with an empty cache")
		tokenCacheItems = map[string]gocache.Item{}
	}
	tokenCache = creationCache{
		cache: gocache.NewFrom(tokenExpiration, 24*time.Hour, tokenCacheItems),
	}

	duration := time.Since(start).Milliseconds()
	durationString := strconv.FormatInt(duration, 10) + "ms"
	log.WithField("duration", durationString).Info("Initiated caches")
}

func initClients(ctx context.Context, config config) {
	log.Info("Initiating clients...")
	start := time.Now()

	ytsClientOpts := imdb2torrent.NewYTSclientOpts(config.BaseURLyts, timeout, config.CacheAgeTorrents)
	tpbClientOpts := imdb2torrent.NewTPBclientOpts(config.BaseURLtpb, config.SocksProxyAddrTPB, timeout, config.CacheAgeTorrents)
	leetxClientOpts := imdb2torrent.NewLeetxClientOpts(config.BaseURL1337x, timeout, config.CacheAgeTorrents)
	ibitClientOpts := imdb2torrent.NewIbitClientOpts(config.BaseURLibit, timeout, config.CacheAgeTorrents)
	rdClientOpts := realdebrid.NewClientOpts(config.BaseURLrd, timeout, config.CacheAgeRD, config.ExtraHeadersRD)

	cinemataClient = cinemata.NewClient(ctx, cinemata.DefaultClientOpts, cinemataCache)
	tpbClient, err := imdb2torrent.NewTPBclient(ctx, tpbClientOpts, torrentCache, cinemataClient)
	if err != nil {
		log.WithError(err).Fatal("Couldn't create TPB client")
	}
	siteClients := map[string]imdb2torrent.MagnetSearcher{
		"YTS":   imdb2torrent.NewYTSclient(ctx, ytsClientOpts, torrentCache),
		"TPB":   tpbClient,
		"1337X": imdb2torrent.NewLeetxClient(ctx, leetxClientOpts, torrentCache, cinemataClient),
		"ibit":  imdb2torrent.NewIbitClient(ctx, ibitClientOpts, torrentCache),
	}
	searchClient = imdb2torrent.NewClient(ctx, siteClients, timeout)
	conversionClient, err = realdebrid.NewClient(ctx, rdClientOpts, tokenCache, availabilityCache)
	if err != nil {
		log.WithError(err).Fatal("Couldn't create RealDebrid client")
	}

	duration := time.Since(start).Milliseconds()
	durationString := strconv.FormatInt(duration, 10) + "ms"
	log.WithField("duration", durationString).Info("Initiated clients")
}
