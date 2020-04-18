package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
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
	Description: "Looks up your selected movie on YTS, The Pirate Bay, 1337x and ibit and automatically turns your selected torrent into a debrid/cached stream, for high speed and no P2P uploading (!). Currently supported providers: real-debrid.com (more coming in the future!).",
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

// In-memory cache, which is filled from a file on startup and persisted to a file in regular intervals.
// Use four different caches so that for example a high churn (new entries pushing out old ones) in the torrent cache doesn't lead to important redirect entries to be lost before used by the user.
var (
	torrentCache      *fastcache.Cache
	tokenCache        *fastcache.Cache
	availabilityCache *fastcache.Cache
	redirectCache     *fastcache.Cache
	cinemataCache     *fastcache.Cache
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
}

func main() {
	mainCtx := context.Background()
	log.Info("Parsing config...")
	config := parseConfig(mainCtx)
	configJSON, err := json.Marshal(config)
	if err != nil {
		log.WithError(err).Fatal("Couldn't marshal config to JSON")
	}

	setLogLevel(config)

	log.WithField("config", string(configJSON)).Info("Parsed config")

	// Load or create caches

	if config.CachePath == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			log.WithError(err).Fatal("Couldn't determine user cache directory via `os.UserCacheDir()`")
		}
		config.CachePath = userCacheDir + "/deflix-stremio"
	} else {
		config.CachePath = strings.TrimSuffix(config.CachePath, "/")
	}
	config.CachePath += "/cache"
	cacheMaxBytes := config.CacheMaxMB * 1000 * 1000
	tokenCache = fastcache.LoadFromFileOrNew(config.CachePath+"/token", cacheMaxBytes/5)
	availabilityCache = fastcache.LoadFromFileOrNew(config.CachePath+"/availability", cacheMaxBytes/5)
	torrentCache = fastcache.LoadFromFileOrNew(config.CachePath+"/torrent", cacheMaxBytes/5)
	redirectCache = fastcache.LoadFromFileOrNew(config.CachePath+"/redirect", cacheMaxBytes/5)
	cinemataCache = fastcache.LoadFromFileOrNew(config.CachePath+"/cinemata", cacheMaxBytes/5)

	// Create clients

	timeout := 5 * time.Second
	cinemataClient := cinemata.NewClient(mainCtx, timeout, cinemataCache)
	tpbClient, err := imdb2torrent.NewTPBclient(mainCtx, config.BaseURLtpb, config.SocksProxyAddrTPB, timeout, torrentCache, config.CacheAgeTorrents, cinemataClient)
	if err != nil {
		log.WithError(err).Fatal("Couldn't create TPB client")
	}
	siteClients := map[string]imdb2torrent.MagnetSearcher{
		"YTS":   imdb2torrent.NewYTSclient(mainCtx, config.BaseURLyts, timeout, torrentCache, config.CacheAgeTorrents),
		"TPB":   tpbClient,
		"1337X": imdb2torrent.NewLeetxclient(mainCtx, config.BaseURL1337x, timeout, torrentCache, cinemataClient, config.CacheAgeTorrents),
		"ibit":  imdb2torrent.NewIbitClient(mainCtx, config.BaseURLibit, timeout, torrentCache, config.CacheAgeTorrents),
	}
	searchClient := imdb2torrent.NewClient(mainCtx, siteClients, timeout)
	conversionClient, err := realdebrid.NewClient(mainCtx, timeout, tokenCache, availabilityCache, config.CacheAgeRD, config.BaseURLrd, config.ExtraHeadersRD)
	if err != nil {
		log.WithError(err).Fatal("Couldn't create RealDebrid client")
	}

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
	// Requires URL query: "?imdbid=123&apitoken=foo"
	caches := map[string]*fastcache.Cache{
		"token":        tokenCache,
		"availability": availabilityCache,
		"torrent":      torrentCache,
		"redirect":     redirectCache,
		"cinemata":     cinemataCache,
	}
	s.HandleFunc("/status", createStatusHandler(mainCtx, searchClient.GetMagnetSearchers(), conversionClient, caches))

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
			persistCache(mainCtx, config.CachePath, stoppingPtr)
		}
	}()

	// Print cache stats every hour
	go func() {
		// Don't run at the same time as the persistence
		time.Sleep(time.Minute)
		stats := fastcache.Stats{}
		for {
			tokenCache.UpdateStats(&stats)
			logCacheStats(mainCtx, stats, "token")
			stats.Reset()
			availabilityCache.UpdateStats(&stats)
			logCacheStats(mainCtx, stats, "availability")
			stats.Reset()
			torrentCache.UpdateStats(&stats)
			logCacheStats(mainCtx, stats, "torrent")
			stats.Reset()
			redirectCache.UpdateStats(&stats)
			logCacheStats(mainCtx, stats, "redirect")
			stats.Reset()
			cinemataCache.UpdateStats(&stats)
			logCacheStats(mainCtx, stats, "cinemata")
			stats.Reset()

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

func persistCache(ctx context.Context, cacheFilePath string, stoppingPtr *bool) {
	if *stoppingPtr {
		log.Warn("Regular cache persistence triggered, but server is shutting down")
		return
	}

	log.WithField("cacheFilePath", cacheFilePath).Info("Persisting caches...")
	if err := tokenCache.SaveToFileConcurrent(cacheFilePath+"/token", runtime.NumCPU()); err != nil {
		log.WithError(err).WithField("cache", "token").Error("Couldn't save cache to file")
	}
	if err := availabilityCache.SaveToFileConcurrent(cacheFilePath+"/availability", runtime.NumCPU()); err != nil {
		log.WithError(err).WithField("cache", "availability").Error("Couldn't save cache to file")
	}
	if err := torrentCache.SaveToFileConcurrent(cacheFilePath+"/torrent", runtime.NumCPU()); err != nil {
		log.WithError(err).WithField("cache", "torrent").Error("Couldn't save cache to file")
	}
	if err := redirectCache.SaveToFileConcurrent(cacheFilePath+"/redirect", runtime.NumCPU()); err != nil {
		log.WithError(err).WithField("cache", "redirect").Error("Couldn't save cache to file")
	}
	if err := cinemataCache.SaveToFileConcurrent(cacheFilePath+"/cinemata", runtime.NumCPU()); err != nil {
		log.WithError(err).WithField("cache", "cinemata").Error("Couldn't save cache to file")
	}
	log.Info("Persisted caches")
}

func logCacheStats(ctx context.Context, stats fastcache.Stats, cacheName string) {
	fields := log.Fields{
		"cache":        cacheName,
		"GetCalls":     stats.GetCalls,
		"SetCalls":     stats.SetCalls,
		"Misses":       stats.Misses,
		"Collisions":   stats.Collisions,
		"Corruptions":  stats.Corruptions,
		"EntriesCount": stats.EntriesCount,
		"Size":         strconv.FormatUint(stats.BytesSize/uint64(1024)/uint64(1024), 10) + "MB",
	}
	log.WithFields(fields).Info("Cache stats")
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
