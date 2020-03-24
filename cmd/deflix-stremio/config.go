package main

import (
	"context"
	"flag"
	"os"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type config struct {
	BindAddr         string        `json:"bindAddr"`
	Port             int           `json:"port"`
	StreamURLaddr    string        `json:"streamURLaddr"`
	CachePath        string        `json:"cachePath"`
	CacheMaxMB       int           `json:"cacheMaxMB"`
	CacheAgeRD       time.Duration `json:"cacheAgeRD"`
	CacheAgeTorrents time.Duration `json:"cacheAgeTorrents"`
	BaseURLyts       string        `json:"baseURLyts"`
	BaseURLtpb       string        `json:"baseURLtpb"`
	BaseURL1337x     string        `json:"baseURL1337x"`
	BaseURLibit      string        `json:"baseURLibit"`
	BaseURLrd        string        `json:"baseURLrd"`
	LogLevel         string        `json:"logLevel"`
	RootURL          string        `json:"rootURL"`
	TPBretries       int           `json:"tpbRetries"`
	ExtraHeaderRD    string        `json:"extraHeaderRD"`
	EnvPrefix        string        `json:"envPrefix"`
}

func parseConfig(ctx context.Context) config {
	result := config{}

	// Flags
	var (
		bindAddr      = flag.String("bindAddr", "localhost", `Local interface address to bind to. "localhost" only allows access from the local host. "0.0.0.0" binds to all network interfaces.`)
		port          = flag.Int("port", 8080, "Port to listen on")
		streamURLaddr = flag.String("streamURLaddr", "http://localhost:8080", "Address to be used in a stream URL that's delivered to Stremio and later used to redirect to RealDebrid")
		cachePath     = flag.String("cachePath", "", "Path for loading a persisted cache on startup and persisting the current cache in regular intervals. An empty value will lead to `os.UserCacheDir()+\"/deflix-stremio/\"`.")
		// We split this number into 5 equal sized caches à 32 MB.
		// Note: fastcache uses 32 MB as minimum, that's why we use `5*32 MB = 160 MB` as minimum.
		cacheMaxMB       = flag.Int("cacheMaxMB", 160, "Max number of megabytes to be used for the in-memory cache. Default (and minimum!) is 160 MB.")
		cacheAgeRD       = flag.Duration("cacheAgeRD", 24*time.Hour, "Max age of cache entries for instant availability responses from RealDebrid. The format must be acceptable by Go's `time.ParseDuration`, for example \"24h\".")
		cacheAgeTorrents = flag.Duration("cacheAgeTorrents", 24*time.Hour, "Max age of cache entries for torrents found per IMDb ID. The format must be acceptable by Go's `time.ParseDuration`, for example \"24h\".")
		baseURLyts       = flag.String("baseURLyts", "https://yts.mx", "Base URL for YTS")
		baseURLtpb       = flag.String("baseURLtpb", "https://thepiratebay.org", "Base URL for TPB")
		baseURL1337x     = flag.String("baseURL1337x", "https://1337x.to", "Base URL for 1337x")
		baseURLibit      = flag.String("baseURLibit", "https://ibit.am", "Base URL for ibit")
		baseURLrd        = flag.String("baseURLrd", "https://api.real-debrid.com", "Base URL for RealDebrid")
		logLevel         = flag.String("logLevel", "debug", `Log level to show only logs with the given and more severe levels. Can be "trace", "debug", "info", "warn", "error", "fatal", "panic".`)
		rootURL          = flag.String("rootURL", "https://www.deflix.tv", "Redirect target for the root")
		tpbRetries       = flag.Int("tpbRetries", 0, "Number of retries in case TPB times out. Each retry will be done after the previous connection is closed.")
		extraHeaderRD    = flag.String("extraHeaderRD", "", "Additional HTTP request header to set for request to RealDebrid")
		envPrefix        = flag.String("envPrefix", "", "Prefix for environment variables")
	)

	flag.Parse()

	if *envPrefix != "" && !strings.HasSuffix(*envPrefix, "_") {
		*envPrefix += "_"
	}
	result.EnvPrefix = *envPrefix

	// Only overwrite the values by their env var counterparts that have not been set (and that *are* set via env var).
	var err error
	if !isArgSet(ctx, "bindAddr") {
		if val, ok := os.LookupEnv(*envPrefix + "BIND_ADDR"); ok {
			*bindAddr = val
		}
	}
	result.BindAddr = *bindAddr

	if !isArgSet(ctx, "port") {
		if val, ok := os.LookupEnv(*envPrefix + "PORT"); ok {
			if *port, err = strconv.Atoi(val); err != nil {
				log.WithError(err).WithField("envVar", "PORT").Fatal("Couldn't convert environment variable from string to int")
			}
		}
	}
	result.Port = *port

	if !isArgSet(ctx, "streamURLaddr") {
		if val, ok := os.LookupEnv(*envPrefix + "STREAM_URL_ADDR"); ok {
			*streamURLaddr = val
		}
	}
	result.StreamURLaddr = *streamURLaddr

	if !isArgSet(ctx, "cachePath") {
		if val, ok := os.LookupEnv(*envPrefix + "CACHE_PATH"); ok {
			*cachePath = val
		}
	}
	result.CachePath = *cachePath

	if !isArgSet(ctx, "cacheMaxMB") {
		if val, ok := os.LookupEnv(*envPrefix + "CACHE_MAX_MB"); ok {
			if *cacheMaxMB, err = strconv.Atoi(val); err != nil {
				log.WithError(err).WithField("envVar", "CACHE_MAX_MB").Fatal("Couldn't convert environment variable from string to int")
			}
		}
	}
	result.CacheMaxMB = *cacheMaxMB

	if !isArgSet(ctx, "cacheAgeRD") {
		if val, ok := os.LookupEnv(*envPrefix + "CACHE_AGE_RD"); ok {
			if *cacheAgeRD, err = time.ParseDuration(val); err != nil {
				log.WithError(err).WithField("envVar", "CACHE_AGE_RD").Fatal("Couldn't convert environment variable from string to time.Duration")
			}
		}
	}
	result.CacheAgeRD = *cacheAgeRD

	if !isArgSet(ctx, "cacheAgeTorrents") {
		if val, ok := os.LookupEnv(*envPrefix + "CACHE_AGE_TORRENTS"); ok {
			if *cacheAgeTorrents, err = time.ParseDuration(val); err != nil {
				log.WithError(err).WithField("envVar", "CACHE_AGE_TORRENTS").Fatal("Couldn't convert environment variable from string to time.Duration")
			}
		}
	}
	result.CacheAgeTorrents = *cacheAgeTorrents

	if !isArgSet(ctx, "baseURLyts") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_YTS"); ok {
			*baseURLyts = val
		}
	}
	result.BaseURLyts = *baseURLyts

	if !isArgSet(ctx, "baseURLtpb") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_TPB"); ok {
			*baseURLtpb = val
		}
	}
	result.BaseURLtpb = *baseURLtpb

	if !isArgSet(ctx, "baseURL1337x") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_1337X"); ok {
			*baseURL1337x = val
		}
	}
	result.BaseURL1337x = *baseURL1337x

	if !isArgSet(ctx, "baseURLibit") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_IBIT"); ok {
			*baseURLibit = val
		}
	}
	result.BaseURLibit = *baseURLibit

	if !isArgSet(ctx, "baseURLrd") {
		if val, ok := os.LookupEnv(*envPrefix + "BASE_URL_RD"); ok {
			*baseURLrd = val
		}
	}
	result.BaseURLrd = *baseURLrd

	if !isArgSet(ctx, "logLevel") {
		if val, ok := os.LookupEnv(*envPrefix + "LOG_LEVEL"); ok {
			*logLevel = val
		}
	}
	result.LogLevel = *logLevel

	if !isArgSet(ctx, "tpbRetries") {
		if val, ok := os.LookupEnv(*envPrefix + "TPB_RETRIES"); ok {
			if *tpbRetries, err = strconv.Atoi(val); err != nil {
				log.WithError(err).WithField("envVar", "TPB_RETRIES").Fatal("Couldn't convert environment variable from string to int")
			}
		}
	}
	result.TPBretries = *tpbRetries

	if !isArgSet(ctx, "rootURL") {
		if val, ok := os.LookupEnv(*envPrefix + "ROOT_URL"); ok {
			*rootURL = val
		}
	}
	result.RootURL = *rootURL

	if !isArgSet(ctx, "extraHeaderRD") {
		if val, ok := os.LookupEnv(*envPrefix + "EXTRA_HEADER_RD"); ok {
			*extraHeaderRD = val
		}
	}
	result.ExtraHeaderRD = *extraHeaderRD

	return result
}

// isArgSet returns true if the argument you're looking for is actually set as command line argument.
// Pass without "-" prefix.
func isArgSet(ctx context.Context, arg string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == arg {
			found = true
		}
	})
	return found
}
