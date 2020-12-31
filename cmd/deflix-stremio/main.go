package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v2"
	"github.com/go-redis/redis/v8"
	"github.com/gofiber/fiber/v2"
	"github.com/markbates/pkger"
	gocache "github.com/patrickmn/go-cache"
	"github.com/spf13/afero"
	"go.uber.org/multierr"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"

	"github.com/deflix-tv/go-stremio"
	"github.com/deflix-tv/go-stremio/pkg/cinemeta"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/alldebrid"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/premiumize"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/realdebrid"
	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
	"github.com/doingodswork/deflix-stremio/pkg/logadapter"
	"github.com/doingodswork/deflix-stremio/pkg/metafetcher"
)

const (
	version = "0.10.2"
)

var (
	manifest = stremio.Manifest{
		ID:          "tv.deflix.stremio",
		Name:        "Deflix - Debrid flicks",
		Description: "Finds movies on YTS, The Pirate Bay, 1337x, RARBG and ibit and automatically turns them into cached HTTP streams with a debrid service like RealDebrid, AllDebrid or Premiumize, for high speed 4k streaming and no P2P uploading (!). For more info see https://www.deflix.tv",
		Version:     version,

		ResourceItems: []stremio.ResourceItem{
			{
				Name:  "stream",
				Types: []string{"movie", "unknown"},
				// Shouldn't be required as long as they're defined globally in the manifest, but some Stremio clients send stream requests for non-IMDb IDs, so maybe setting this here as well helps.
				// The "deflix-" prefix is for debrid service catalog items.
				IDprefixes: []string{"tt", "deflix-"},
			},
			{
				Name: "catalog",
				// All Stremio-supported types that a user could've downloaded to RD/AD/Premiumize. This excludes "channel" (like YouTube channels, so a list of videos) and "tv" (which is live). Custom names are allowed.
				Types: []string{"unknown"},
			},
		},
		Types: []string{"movie", "unknown"},
		// An empty slice is required for serializing to a JSON that Stremio expects.
		// We're altering the manifest and add a catalog (of videos downloaded to RD/AD/Premiumize) if a user configured the addon that way.
		Catalogs: []stremio.CatalogItem{},

		// The "deflix-" prefix is for debrid service catalog items.
		IDprefixes: []string{"tt", "deflix-"},
		// Must use www.deflix.tv instead of just deflix.tv because GitHub takes care of redirecting non-www to www and this leads to HTTPS certificate issues.
		Background: "https://www.deflix.tv/images/Logo-1024px.png",
		Logo:       "https://www.deflix.tv/images/Logo-250px.png",

		BehaviorHints: stremio.BehaviorHints{
			P2P:                   false,
			Configurable:          true,
			ConfigurationRequired: true,
		},
	}

	catalogs = []stremio.CatalogItem{
		{
			Type: "unknown",
			ID:   "rd-torrents",
			Name: "Torrents you added to RealDebrid",
		},
		{
			Type: "unknown",
			ID:   "ad-torrents",
			Name: "Torrents you added to AllDebrid",
		},
		{
			Type: "unknown",
			ID:   "pm-torrents",
			Name: "Torrents you added to Premiumize",
		},
	}
)

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
	pmAvailabilityCache *creationCache
	tokenCache          *creationCache
	// go-cache or Redis, depending on config
	redirectCache *goCache
	streamCache   *goCache
)

// Clients
var (
	metaFetcher  stremio.MetaFetcher
	searchClient *imdb2torrent.Client
	rdClient     *realdebrid.Client
	adClient     *alldebrid.Client
	pmClient     *premiumize.Client
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

	// Parse and validate config

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

	config.validate(logger)
	logger.Info("Validated config")

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
		"availability-rd": rdAvailabilityCache.cache,
		"availability-ad": adAvailabilityCache.cache,
		"availability-pm": pmAvailabilityCache.cache,
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

	catalogHandler := createCatalogHandler(rdClient, adClient, pmClient, logger)
	streamHandler := createStreamHandler(config, searchClient, rdClient, adClient, pmClient, redirectCache, logger)
	catalogStreamHandler := createCatalogStreamHandler(config, searchClient, rdClient, adClient, pmClient, redirectCache, logger)
	catalogHandlers := map[string]stremio.CatalogHandler{"unknown": catalogHandler}
	streamHandlers := map[string]stremio.StreamHandler{"movie": streamHandler, "unknown": catalogStreamHandler}

	var httpFS http.FileSystem
	if config.WebConfigurePath == "" {
		pkgerDir := pkger.Dir("/web/configure")
		mm := afero.NewMemMapFs()
		// Copy all files from pkger to afero memory-mapped FS.
		// This is a workaround so we can *write* a file to it.
		// TODO: Replace all this as soon as Go 1.16 supports embedding files into a binary.
		for _, fName := range []string{"/deflix.css", "/favicon.ico", "/index-apikey.html", "/index-oauth2.html", "/mvp.css"} {
			f, err := pkgerDir.Open(fName)
			if err != nil {
				logger.Fatal("Couldn't open "+fName, zap.Error(err))
			}
			fData, err := ioutil.ReadAll(f)
			if err != nil {
				logger.Fatal("Couldn't read "+fName, zap.Error(err))
			}
			absPath := "/" + fName
			if err = afero.WriteFile(mm, absPath, fData, 0644); err != nil {
				logger.Fatal("Couldn't write to "+absPath, zap.Error(err))
			}
		}

		// Rename one of the index.html files depending on OAuth2 configuration
		var fromPath string
		if config.UseOAUTH2 {
			fromPath = "/index-oauth2.html"
		} else {
			fromPath = "/index-apikey.html"
		}
		from, err := mm.Open(fromPath)
		if err != nil {
			logger.Fatal("Couldn't open "+fromPath, zap.Error(err))
		}
		to, err := mm.Create("/index.html")
		if err != nil {
			logger.Fatal(`Couldn't create "/index.html"`, zap.Error(err))
		}
		fromBytes, err := ioutil.ReadAll(from)
		if err != nil {
			logger.Fatal("Couldn't read "+fromPath, zap.Error(err))
		}
		_, err = to.Write(fromBytes)
		if err != nil {
			logger.Fatal(`Couldn't write "/index.html"`, zap.Error(err))
		}

		// Clean up memory and FS a bit by removing the unnecessary files.
		// FS because we don't want people to access `www.example.com/index-apikey.html` for example.
		if err = mm.Remove("/index-oauth2.html"); err != nil {
			logger.Fatal(`Couldn't remove "/index-oauth2.html"`, zap.Error(err))
		}
		if err = mm.Remove("/index-apikey.html"); err != nil {
			logger.Fatal(`Couldn't remove "/index-apikey.html"`, zap.Error(err))
		}
		httpFS = afero.NewHttpFs(mm)
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
		// We already have a metaFetcher Client
		MetaClient:      metaFetcher,
		ConfigureHTMLfs: httpFS,
		// IMDb IDs for regular streams and "deflix-" for debrid service catalog items
		StreamIDregex: "(tt\\d{7,8}|deflix-.+)",
	}

	// Create addon

	addon, err := stremio.NewAddon(manifest, catalogHandlers, streamHandlers, options)
	if err != nil {
		logger.Fatal("Couldn't create new addon", zap.Error(err))
	}

	// Customize addon

	confPM := oauth2.Config{
		ClientID:     config.OAUTH2clientIDpm,
		ClientSecret: config.OAUTH2clientSecretPM,
		Endpoint: oauth2.Endpoint{
			AuthURL:  config.OAUTH2authorizeURLpm,
			TokenURL: config.OAUTH2tokenURLpm,
		},
	}

	logger.Info("Starting to hash the OAuth2 encryption key...")
	hashStart := time.Now()
	// Default bcrypt "cost" is 10, but we're only hashing this one time at startup, so we can spend a second or so.
	bcryptKey, err := bcrypt.GenerateFromPassword([]byte(config.OAUTH2encryptionKey), 14)
	if err != nil {
		logger.Fatal("Couldn't hash OAuth2 encryption key via bcrypt", zap.Error(err))
	}
	logger.Info("Finished hashing the OAuth2 encryption key.", zap.Duration("duration", time.Since(hashStart)))
	// The bcrypt result is 60 bytes. We want 32 bytes for AES-256. The initial bytes in bcrypt are the same, so we use the last ones.
	aesKey := bcryptKey[28:60]
	authMiddleware := createAuthMiddleware(rdClient, adClient, pmClient, config.UseOAUTH2, confPM, aesKey, logger)
	addon.AddMiddleware("/:userData/manifest.json", authMiddleware)
	addon.AddMiddleware("/:userData/stream/:type/:id.json", authMiddleware)
	addon.AddMiddleware("/:userData/redirect/:id", authMiddleware)
	// No need to set the middleware to the stream route without user data because go-stremio blocks it (with a 400 Bad Request response) if BehaviorHints.ConfigurationRequired is true.

	manifestCallback := func(ctx context.Context, m *stremio.Manifest, userDataIface interface{}) int {
		// The middleware already took care of validating the user data
		udString := userDataIface.(string)
		userData, _ := decodeUserData(udString, logger)
		// If the user wants RD/AD/Premiumize downloads as catalog, we add the catalog to the manifest
		if userData.Catalog {
			if userData.RDtoken != "" {
				fmt.Printf("============= adding catalog to manifest")
				m.Catalogs = append(m.Catalogs, catalogs[0])
			} else if userData.ADkey != "" {
				m.Catalogs = append(m.Catalogs, catalogs[1])
			} else {
				m.Catalogs = append(m.Catalogs, catalogs[2])
			}
		}
		return fiber.StatusOK
	}
	addon.SetManifestCallback(manifestCallback)

	// Requires URL query: "?imdbid=123&apitoken=foo"
	statusEndpoint := createStatusHandler(searchClient.GetMagnetSearchers(), rdClient, adClient, pmClient, goCaches, logger)
	addon.AddEndpoint("GET", "/status", statusEndpoint)

	// Redirects stream URLs (previously sent to Stremio) to the actual RealDebrid stream URLs
	redirHandler := createRedirectHandler(redirectCache, streamCache, rdClient, adClient, pmClient, logger)
	addon.AddEndpoint("GET", "/:userData/redirect/:id", redirHandler)
	// Stremio sends a HEAD request before starting a stream.
	addon.AddEndpoint("HEAD", "/:userData/redirect/:id", redirHandler)

	// For OAuth2 redirect handling for Premiumize
	isHTTPS := strings.HasPrefix(config.StreamURLaddr, "https")
	oauth2initHandler := createOAUTH2initHandler(confPM, isHTTPS, logger)
	addon.AddEndpoint("GET", "/oauth2/init", oauth2initHandler)
	oauth2installHandler := createOAUTH2installHandler(confPM, aesKey, logger)
	addon.AddEndpoint("GET", "/oauth2/install", oauth2installHandler)

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
	badgerLogger := logadapter.NewBadger2Zap(logger)
	options := badger.DefaultOptions(config.StoragePath).
		WithLogger(badgerLogger).
		WithLoggingLevel(badger.WARNING).
		WithSyncWrites(false)
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

	pmAvailabilityCacheItems, err := loadGoCache(config.CachePath + "/availability-pm.gob")
	if err != nil {
		logger.Error("Couldn't load Premiumize availability cache from file - continuing with an empty cache", zap.Error(err))
		pmAvailabilityCacheItems = map[string]gocache.Item{}
	}
	pmAvailabilityCache = &creationCache{
		cache: gocache.NewFrom(config.CacheAgeXD, 24*time.Hour, pmAvailabilityCacheItems),
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

	// TODO: Return closer func like in the stores initialization function.
	var err error
	cinemetaClient := cinemeta.NewClient(cinemeta.DefaultClientOpts, cinemetaCache, logger)
	metaFetcher, err = metafetcher.NewClient(config.IMDB2metaAddr, cinemetaClient, logger)
	if err != nil {
		logger.Error("Couldn't create metafetcher client, continuing with regular cinemetaClient", zap.Error(err))
		metaFetcher = cinemetaClient
	}

	ytsClientOpts := imdb2torrent.NewYTSclientOpts(config.BaseURLyts, timeout, config.MaxAgeTorrents)
	tpbClientOpts := imdb2torrent.NewTPBclientOpts(config.BaseURLtpb, config.SocksProxyAddrTPB, timeout, config.MaxAgeTorrents)
	leetxClientOpts := imdb2torrent.NewLeetxClientOpts(config.BaseURL1337x, timeout, config.MaxAgeTorrents)
	ibitClientOpts := imdb2torrent.NewIbitClientOpts(config.BaseURLibit, timeout, config.MaxAgeTorrents)
	rarbgClientOpts := imdb2torrent.NewRARBGclientOpts(config.BaseURLrarbg, timeout, config.MaxAgeTorrents)
	rdClientOpts := realdebrid.NewClientOpts(config.BaseURLrd, timeout, config.CacheAgeXD, config.ExtraHeadersXD)
	adClientOpts := alldebrid.NewClientOpts(config.BaseURLad, timeout, config.CacheAgeXD, config.ExtraHeadersXD)
	pmClientOpts := premiumize.NewClientOpts(config.BaseURLpm, timeout, config.CacheAgeXD, config.ExtraHeadersXD, config.UseOAUTH2)

	tpbClient, err := imdb2torrent.NewTPBclient(tpbClientOpts, torrentCache, metaFetcher, logger, config.LogFoundTorrents)
	if err != nil {
		logger.Fatal("Couldn't create TPB client", zap.Error(err))
	}
	siteClients := map[string]imdb2torrent.MagnetSearcher{
		"YTS":   imdb2torrent.NewYTSclient(ytsClientOpts, torrentCache, logger, config.LogFoundTorrents),
		"TPB":   tpbClient,
		"1337X": imdb2torrent.NewLeetxClient(leetxClientOpts, torrentCache, metaFetcher, logger, config.LogFoundTorrents),
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
	pmClient, err = premiumize.NewClient(pmClientOpts, tokenCache, pmAvailabilityCache, logger)
	if err != nil {
		logger.Fatal("Couldn't create Premiumize client", zap.Error(err))
	}

	duration := time.Since(start).Milliseconds()
	durationString := strconv.FormatInt(duration, 10) + "ms"
	logger.Info("Initialized clients", zap.String("duration", durationString))
}
