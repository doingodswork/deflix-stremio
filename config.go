package main

import (
	"flag"
	"log"
	"os"
	"strconv"
	"strings"
)

// Flags
var (
	bindAddr      = *flag.String("bindAddr", "localhost", "Local interface address to bind to. \"0.0.0.0\" binds to all interfaces.")
	port          = *flag.Int("port", 8080, "Port to listen on")
	streamURLaddr = *flag.String("streamURLaddr", "http://localhost:8080", "Address to be used in a stream URL that's delivered to Stremio and later used to redirect to RealDebrid")
	cachePath     = *flag.String("cachePath", "", "Path for loading a persisted cache on startup and persisting the current cache in regular intervals. An empty value will lead to `os.UserCacheDir()+\"/deflix-stremio/\"`")
	// 128*1024*1024 MB = 128 MB
	// We split these on 4 caches à 32 MB
	// Note: fastcache uses 32 MB as minimum, that's why we use `4*32 MB = 128 MB` as minimum.
	cacheMaxBytes = *flag.Int("cacheMaxBytes", 128*1024*1024, "Max number of bytes to be used for the in-memory cache. Default (and minimum!) is 128 MB.")
	baseURLyts    = *flag.String("baseURLyts", "https://yts.mx", "Base URL for YTS")
	baseURLtpb    = *flag.String("baseURLtpb", "https://thepiratebay.org", "Base URL for TPB")
	baseURL1337x  = *flag.String("baseURL1337x", "https://1337x.to", "Base URL for 1337x")
	envPrefix     = *flag.String("envPrefix", "", "Prefix for environment variables")
)

func parseConfig() {
	flag.Parse()

	if envPrefix != "" && !strings.HasSuffix(envPrefix, "_") {
		envPrefix += "_"
	}

	// Only overwrite the values by their env var counterparts that have not been set (and that *are* set via env var).
	var err error
	if !isArgSet("bindAddr") {
		if val, ok := os.LookupEnv(envPrefix + "BIND_ADDR"); ok {
			bindAddr = val
		}
	}
	if !isArgSet("port") {
		if val, ok := os.LookupEnv(envPrefix + "PORT"); ok {
			if port, err = strconv.Atoi(val); err != nil {
				log.Fatal("Couldn't convert environment variable PORT from string to int")
			}
		}
	}
	if !isArgSet("streamURLaddr") {
		if val, ok := os.LookupEnv(envPrefix + "STREAM_URL_ADDR"); ok {
			streamURLaddr = val
		}
	}
	if !isArgSet("cachePath") {
		if val, ok := os.LookupEnv(envPrefix + "CACHE_PATH"); ok {
			cachePath = val
		}
	}
	if !isArgSet("cacheMaxBytes") {
		if val, ok := os.LookupEnv(envPrefix + "CACHE_MAX_BYTES"); ok {
			if cacheMaxBytes, err = strconv.Atoi(val); err != nil {
				log.Fatal("Couldn't convert environment variable CACHE_MAX_BYTES from string to int")
			}
		}
	}
	if !isArgSet("baseURLyts") {
		if val, ok := os.LookupEnv(envPrefix + "BASE_URL_YTS"); ok {
			baseURLyts = val
		}
	}
	if !isArgSet("baseURLtpb") {
		if val, ok := os.LookupEnv(envPrefix + "BASE_URL_TPB"); ok {
			baseURLtpb = val
		}
	}
	if !isArgSet("baseURL1337x") {
		if val, ok := os.LookupEnv(envPrefix + "BASE_URL_1337X"); ok {
			baseURL1337x = val
		}
	}
}

// isArgSet returns true if the argument you're looking for is actually set as command line argument.
// Pass without "-" prefix.
func isArgSet(arg string) bool {
	arg = "-" + arg
	for _, argsElem := range flag.Args() {
		if arg == argsElem {
			return true
		}
	}
	return false
}
