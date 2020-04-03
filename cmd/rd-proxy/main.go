package main

import (
	"context"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	bindAddr     = flag.String("bindAddr", "localhost", `Local interface address to bind to. "localhost" only allows access from the local host. "0.0.0.0" binds to all network interfaces.`)
	port         = flag.Int("port", 8080, "Port to listen on")
	targetURL    = flag.String("targetURL", "https://api.real-debrid.com", "Reverse proxy target URL")
	apiKeyHeader = flag.String("apiKeyHeader", "", "Header key for the API key, e.g. \"X-Proxy-Apikey\"")
	apiKeys      = flag.String("apiKeys", "", "List of comma separated API keys that the reverse proxy allows")
	logRequest   = flag.Bool("logRequest", false, "Log the full request object")
)

func init() {
	// Make predicting "random" numbers harder
	rand.NewSource(time.Now().UnixNano())
}

func main() {
	mainCtx := context.Background()
	flag.Parse()

	// Precondition checks
	if *targetURL == "" {
		log.Fatal("targetURL CLI argument must not be empty")
	}
	if (*apiKeyHeader == "" && *apiKeys != "") || (*apiKeyHeader != "" && *apiKeys == "") {
		log.Fatal("apiKeyHeader and apiKeys CLI arguments must either both be empty or both not empty")
	}

	// Clean up API keys input
	var apiKeyList []string
	if *apiKeys != "" {
		apiKeyList = strings.Split(*apiKeys, ",")
		i := 0
		for _, apiKey := range apiKeyList {
			apiKey = strings.TrimSpace(apiKey)
			// Skip empty elements
			if apiKey != "" {
				apiKeyList[i] = apiKey
				i += 1
			}
		}
		// All non-empty elements have been moved to the beginning. Cut off the rest.
		apiKeyList = apiKeyList[:i]
		log.Printf("Accepted API keys: %v\n", apiKeyList)
	} else {
		log.Printf("Reverse proxy not secured by API keys")
	}

	// Create handler
	handler := createHandler(mainCtx, *targetURL, *apiKeyHeader, apiKeyList)

	// Serve requests
	http.HandleFunc("/", handler)

	srv := &http.Server{
		Addr:    *bindAddr + ":" + strconv.Itoa(*port),
		Handler: http.DefaultServeMux,
		// Timeouts to avoid Slowloris attacks
		ReadTimeout:    time.Second * 5,
		WriteTimeout:   time.Second * 15,
		IdleTimeout:    time.Second * 60,
		MaxHeaderBytes: 1 * 1000, // 1 KB
	}

	stopping := false
	stoppingPtr := &stopping
	log.Println("Starting server")
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			if !*stoppingPtr {
				log.Fatalf("Couldn't start server: %v", err)
			} else {
				log.Fatalf("Error in srv.ListenAndServe() during server shutdown (probably context deadline expired before the server could shutdown cleanly): %v", err)
			}
		}
	}()

	// Graceful shutdown

	c := make(chan os.Signal, 1)
	// Accept SIGINT (Ctrl+C) and SIGTERM (`docker stop`)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	sig := <-c
	log.Printf("Received signal \"%s\", shutting down...", sig)
	*stoppingPtr = true
	// Create a deadline to wait for. `docker stop` gives us 10 seconds.
	// No need to get the cancel func and defer calling it, because srv.Shutdown() will consider the timeout from the context.
	mainCtx, _ = context.WithTimeout(mainCtx, 9*time.Second)
	// Doesn't block if no connections, but will otherwise wait until the timeout deadline
	if err := srv.Shutdown(mainCtx); err != nil {
		log.Fatalf("Error shutting down server: %v", err)
	}
	log.Println("Server shut down")
}

func createHandler(ctx context.Context, targetURL, apiKeyHeader string, allowedAPIkeys []string) func(http.ResponseWriter, *http.Request) {
	// Create reverse proxy
	target, err := url.Parse(targetURL)
	if err != nil {
		log.Fatalf("Couldn't parse reverse proxy target URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	return func(w http.ResponseWriter, r *http.Request) {
		// API key check only if required
		if apiKeyHeader != "" {
			apiKey := r.Header.Get(apiKeyHeader)
			if apiKey == "" {
				log.Printf("Got request without API key from %v\n", r.RemoteAddr)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			ok := false
			for _, allowedKey := range allowedAPIkeys {
				if apiKey == allowedKey {
					ok = true
					break
				}
			}
			if !ok {
				log.Printf("Got request with invalid API key from %v\n", r.RemoteAddr)
				w.WriteHeader(http.StatusForbidden)
				return
			}

			// Remove API key header from request
			r.Header.Del(apiKeyHeader)
		}

		r.Host = target.Host
		// Info: req.URL.Scheme, req.URL.Host and req.URL.Path are set to the target's value in the ReverseProxy's "Director".

		// Remove all headers that CloudFlare might have set, except Authorization and User-Agent.
		for headerKey := range r.Header {
			if headerKey == "Authorization" || headerKey == "User-Agent" {
				continue
			}
			r.Header.Del(headerKey)
		}

		// Add random IP as "X-Forwarded-For"
		r.Header.Set("X-Forwarded-For", randIP())

		if *logRequest {
			log.Printf("Proxying request from %v. Request: %+v\n", r.RemoteAddr, r)
		} else {
			log.Printf("Proxying request from %v\n", r.RemoteAddr)
		}

		proxy.ServeHTTP(w, r)
	}
}

func randIP() string {
	return randIPpart() + "." + randIPpart() + "." + randIPpart() + "." + randIPpart()
}

func randIPpart() string {
	// Between 2 and 254
	randNo := rand.Intn(253) + 2
	return strconv.Itoa(randNo)
}
