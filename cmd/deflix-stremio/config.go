package main

import (
	"flag"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type config struct {
	BindAddr          string        `json:"bindAddr"`
	Port              int           `json:"port"`
	StreamURLaddr     string        `json:"streamURLaddr"`
	CachePath         string        `json:"cachePath"`
	CacheMaxMB        int           `json:"cacheMaxMB"`
	CacheAgeXD        time.Duration `json:"cacheAgeXD"`
	CacheAgeTorrents  time.Duration `json:"cacheAgeTorrents"`
	BaseURLyts        string        `json:"baseURLyts"`
	BaseURLtpb        string        `json:"baseURLtpb"`
	BaseURL1337x      string        `json:"baseURL1337x"`
	BaseURLibit       string        `json:"baseURLibit"`
	BaseURLrarbg      string        `json:"baseURLrarbg"`
	BaseURLrd         string        `json:"baseURLrd"`
	BaseURLad         string        `json:"baseURLad"`
	LogLevel          string        `json:"logLevel"`
	LogFoundTorrents  bool          `json:"logFoundTorrents"`
	RootURL           string        `json:"rootURL"`
	ExtraHeadersRD    []string      `json:"extraHeadersRD"`
	SocksProxyAddrTPB string        `json:"socksProxyAddrTPB"`
	EnvPrefix         string        `json:"envPrefix"`
}

func parseConfig(logger *zap.Logger) config {
	result := config{}

	// Flags
	var (
		bindAddr          = flag.String("bindAddr", "localhost", `Local interface address to bind to. "localhost" only allows access from the local host. "0.0.0.0" binds to all network interfaces.`)
		port              = flag.Int("port", 8080, "Port to listen on")
		streamURLaddr     = flag.String("streamURLaddr", "http://localhost:8080", "Address to be used in a stream URL that's delivered to Stremio and later used to redirect to RealDebrid")
		cachePath         = flag.String("cachePath", "", "Path for loading a persisted cache on startup and persisting the current cache in regular intervals. An empty value will lead to 'os.UserCacheDir()+\"/deflix-stremio/\"'.")
		cacheMaxMB        = flag.Int("cacheMaxMB", 32, "Max number of megabytes to be used for the in-memory torrent cache. Default (and minimum!) is 32 MB.")
		cacheAgeXD        = flag.Duration("cacheAgeXD", 24*time.Hour, "Max age of cache entries for instant availability responses from RealDebrid and AllDebrid. The format must be acceptable by Go's 'time.ParseDuration()', for example \"24h\".")
		cacheAgeTorrents  = flag.Duration("cacheAgeTorrents", 24*time.Hour, "Max age of cache entries for torrents found per IMDb ID. The format must be acceptable by Go's 'time.ParseDuration()', for example \"24h\".")
		baseURLyts        = flag.String("baseURLyts", "https://yts.mx", "Base URL for YTS")
		baseURLtpb        = flag.String("baseURLtpb", "https://apibay.org", "Base URL for the TPB API")
		baseURL1337x      = flag.String("baseURL1337x", "https://1337x.to", "Base URL for 1337x")
		baseURLibit       = flag.String("baseURLibit", "https://ibit.am", "Base URL for ibit")
		baseURLrarbg      = flag.String("baseURLrarbg", "https://torrentapi.org", "Base URL for RARBG")
		baseURLrd         = flag.String("baseURLrd", "https://api.real-debrid.com", "Base URL for RealDebrid")
		baseURLad         = flag.String("baseURLad", "https://api.alldebrid.com", "Base URL for AllDebrid")
		logLevel          = flag.String("logLevel", "debug", `Log level to show only logs with the given and more severe levels. Can be "debug", "info", "warn", "error".`)
		logFoundTorrents  = flag.Bool("logFoundTorrents", false, "Set to true to log each single torrent that was found by one of the torrent site clients (with DEBUG level)")
		rootURL           = flag.String("rootURL", "https://www.deflix.tv", "Redirect target for the root")
		extraHeadersRD    = flag.String("extraHeadersRD", "", "Additional HTTP request headers to set for requests to RealDebrid, in a format like \"X-Foo: bar\", separated by newline characters (\"\\n\")")
		socksProxyAddrTPB = flag.String("socksProxyAddrTPB", "", "SOCKS5 proxy address for accessing TPB, required for accessing TPB via the TOR network (where \"127.0.0.1:9050\" would be typical value)")
		envPrefix         = flag.String("envPrefix", "", "Prefix for environment variables")
	)

	flag.Parse()

	if *envPrefix != "" && !strings.HasSuffix(*envPrefix, "_") {
		*envPrefix += "_"
	}
	result.EnvPrefix = *envPrefix

	// Only overwrite the values by their env var counterparts that have not been set (and that *are* set via env var).
	var err error
	if !isArgSet("bindAddr") {
		if val, ok := os.LookupEnv(*envPrefix + "BIND_ADDR"); ok {
			*bindAddr = val
		}
	}
	result.BindAddr = *bindAddr

	if !isArgSet("port") {
		if val, ok := os.LookupEnv(*envPrefix + "PORT"); ok {
			if *port, err = strconv.Atoi(val); err != nil {
				logger.Fatal("Couldn't convert environment variable from string to int", zap.Error(err), zap.String("envVar", "PORT"))
			}
		}
	}
	result.Port = *port

	if !isArgSet("streamURLaddr") {
		if val, ok := os.LookupEnv(*envPrefix + "STREAM_URL_ADDR"); ok {
			*streamURLaddr = val
		}
	}
	result.StreamURLaddr = *streamURLaddr

	if !isArgSet("cachePath") {
		if val, ok := os.LookupEnv(*envPrefix + "CACHE_PATH"); ok {
			*cachePath = val
		}
	}
	result.CachePath = *cachePath

	if !isArgSet("cacheMaxMB") {
		if val, ok := os.LookupEnv(*envPrefix + "CACHE_MAX_MB"); ok {
			if *cacheMaxMB, err = strconv.Atoi(val); err != nil {
				logger.Fatal("Couldn't convert environment variable from string to int", zap.Error(err), zap.String("envVar", "CACHE_MAX_MB"))

			}
		}
	}
	result.CacheMaxMB = *cacheMaxMB

	if !isArgSet("cacheAgeXD") {
		if val, ok := os.LookupEnv(*envPrefix + "CACHE_AGE_XD"); ok {
			if *cacheAgeXD, err = time.ParseDuration(val); err != nil {
				logger.Fatal("Couldn't convert environment variable from string to time.Duration", zap.Error(err), zap.String("envVar", "CACHE_AGE_XD"))
			}
		}
	}
	result.CacheAgeXD = *cacheAgeXD

	if !isArgSet("cacheAgeTorrents") {
		if val, ok := os.LookupEnv(*envPrefix + "CACHE_AGE_TORRENTS"); ok {
			if *cacheAgeTorrents, err = time.ParseDuration(val); err != nil {
				logger.Fatal("Couldn't convert environment variable from string to time.Duration", zap.Error(err), zap.String("envVar", "CACHE_AGE_TORRENTS"))
			}
		}
	}
	result.CacheAgeTorrents = *cacheAgeTorrents

	if !isArgSet("baseURLyts") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_YTS"); ok {
			*baseURLyts = val
		}
	}
	result.BaseURLyts = *baseURLyts

	if !isArgSet("baseURLtpb") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_TPB"); ok {
			*baseURLtpb = val
		}
	}
	result.BaseURLtpb = *baseURLtpb

	if !isArgSet("baseURL1337x") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_1337X"); ok {
			*baseURL1337x = val
		}
	}
	result.BaseURL1337x = *baseURL1337x

	if !isArgSet("baseURLibit") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_IBIT"); ok {
			*baseURLibit = val
		}
	}
	result.BaseURLibit = *baseURLibit

	if !isArgSet("baseURLrarbg") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_RARBG"); ok {
			*baseURLrarbg = val
		}
	}
	result.BaseURLrarbg = *baseURLrarbg

	if !isArgSet("baseURLrd") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_RD"); ok {
			*baseURLrd = val
		}
	}
	result.BaseURLrd = *baseURLrd

	if !isArgSet("baseURLad") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_AD"); ok {
			*baseURLrd = val
		}
	}
	result.BaseURLad = *baseURLad

	if !isArgSet("logLevel") {
		if val, ok := os.LookupEnv(*envPrefix + "LOG_LEVEL"); ok {
			*logLevel = val
		}
	}
	result.LogLevel = *logLevel

	if !isArgSet("logFoundTorrents") {
		if val, ok := os.LookupEnv(*envPrefix + "LOG_FOUND_TORRENTS"); ok {
			if *logFoundTorrents, err = strconv.ParseBool(val); err != nil {
				logger.Fatal("Couldn't convert environment variable from string to bool", zap.Error(err), zap.String("envVar", "LOG_FOUND_TORRENTS"))
			}
		}
	}
	result.LogFoundTorrents = *logFoundTorrents

	if !isArgSet("rootURL") {
		if val, ok := os.LookupEnv(*envPrefix + "ROOT_URL"); ok {
			*rootURL = val
		}
	}
	result.RootURL = *rootURL

	if !isArgSet("extraHeadersRD") {
		if val, ok := os.LookupEnv(*envPrefix + "EXTRA_HEADERS_RD"); ok {
			*extraHeadersRD = val
		}
	}
	if *extraHeadersRD != "" {
		headers := strings.Split(*extraHeadersRD, "\n")
		for _, header := range headers {
			header = strings.TrimSpace(header)
			if header != "" {
				result.ExtraHeadersRD = append(result.ExtraHeadersRD, header)
			}
		}
	}

	if !isArgSet("socksProxyAddrTPB") {
		if val, ok := os.LookupEnv(*envPrefix + "SOCKS_PROXY_ADDR_TPB"); ok {
			*socksProxyAddrTPB = val
		}
	}
	result.SocksProxyAddrTPB = *socksProxyAddrTPB

	return result
}

// isArgSet returns true if the argument you're looking for is actually set as command line argument.
// Pass without "-" prefix.
func isArgSet(arg string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == arg {
			found = true
		}
	})
	return found
}
