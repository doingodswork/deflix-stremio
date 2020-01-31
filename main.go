package main

import (
	"context"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"

	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
	"github.com/doingodswork/deflix-stremio/pkg/realdebrid"
	"github.com/doingodswork/deflix-stremio/pkg/stremio"
)

const (
	version = "0.1.0"
)

// Flags
var (
	streamURLaddr = *flag.String("streamURLaddr", "http://localhost:8080", "Address to be used in a stream URL that's delivered to Stremio and later used to redirect to RealDebrid")
)

var (
	manifest = stremio.Manifest{
		ID:          "tv.deflix.stremio",
		Name:        "Deflix - Debrid flicks",
		Description: "Automatically turns torrents into debrid/cached streams, for high speed and no seeding. Currently supported providers: real-debrid.com (more coming soon™).",
		Version:     version,

		ResourceItems: []stremio.ResourceItem{
			stremio.ResourceItem{
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
)

func init() {
	// Timeout for global default HTTP client (for when using `http.Get()`)
	http.DefaultClient.Timeout = 5 * time.Second

	// Make predicting "random" numbers harder
	rand.NewSource(time.Now().UnixNano())
}

func main() {
	flag.Parse()

	// Basic middleware and health endpoint

	log.Println("Setting up server")
	r := mux.NewRouter()
	s := r.Methods("GET").Subrouter()
	s.Use(timerMiddleware,
		corsMiddleware, // Stremio doesn't show stream responses when no CORS middleware is used!
		handlers.ProxyHeaders,
		recoveryMiddleware,
		loggingMiddleware)
	s.HandleFunc("/health", healthHandler)

	// Stremio endpoints

	conversionClient := realdebrid.NewClient(5 * time.Second)
	searchClient := imdb2torrent.NewClient(5 * time.Second)
	// Maps random IDs to RealDebrid streamable video URLs, used for being able to resolve torrents to streamable URLs in the background while already responding to a Stremio stream request.
	redirectMap := make(map[string]string)
	// Use token middleware only for the Stremio endpoints
	tokenMiddleware := createTokenMiddleware(conversionClient)
	manifestHandler := createManifestHandler(conversionClient)
	streamHandler := createStreamHandler(searchClient, conversionClient, redirectMap)
	s.HandleFunc("/{apitoken}/manifest.json", tokenMiddleware(manifestHandler).ServeHTTP)
	s.HandleFunc("/{apitoken}/stream/{type}/{id}.json", tokenMiddleware(streamHandler).ServeHTTP)

	// Additional endpoints

	// Redirects stream URLs (previously sent to Stremio) to the actual RealDebrid stream URLs
	s.HandleFunc("/redirect/{id}", createRedirectHandler(redirectMap))

	srv := &http.Server{
		Addr:    "0.0.0.0:8080",
		Handler: s,
		// Timeouts to avoid Slowloris attacks
		ReadTimeout:    time.Second * 5,
		WriteTimeout:   time.Second * 15,
		IdleTimeout:    time.Second * 60,
		MaxHeaderBytes: 1 << 10, // 1 KB
	}

	log.Println("Starting server")
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal("Couldn't start server:", err)
		}
	}()

	// Timed logger for easier debugging with logs
	go func() {
		for {
			log.Println("...")
			time.Sleep(time.Second)
		}
	}()

	// Graceful shutdown

	c := make(chan os.Signal, 1)
	// Accept SIGINT (Ctrl+C) and SIGTERM (`docker stop`)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	sig := <-c
	log.Printf("Received \"%v\" signal. Shutting down...\n", sig)
	// Create a deadline to wait for.
	// Use the same value as the server's `WriteTimeout`.
	// No need to get the cancel func and defer calling it, because srv.Shutdown() will consider the timeout from the context.
	ctx, _ := context.WithTimeout(context.Background(), 15*time.Second)
	// Doesn't block if no connections, but will otherwise wait until the timeout deadline
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Error shutting down server:", err)
	}
}
