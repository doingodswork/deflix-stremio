package main

import (
	"log"
	"net/http"
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
	rand.NewSource(time.Now().UnixNano())
}

func main() {
	// Timeout for global default HTTP client (for when using `http.Get()`)
	http.DefaultClient.Timeout = 5 * time.Second

	conversionClient := realdebrid.NewClient(5 * time.Second)
	searchClient := imdb2torrent.NewClient(5 * time.Second)

	// Maps random IDs to RealDebrid streamable video URLs, used for being able to resolve torrents to streamable URLs in the background while already responding to a Stremio stream request.
	redirectMap := make(map[string]string)

	log.Println("Setting up server")
	r := mux.NewRouter()
	r.HandleFunc("/{apitoken}/manifest.json", createManifestHandler(conversionClient))
	r.HandleFunc("/{apitoken}/stream/{type}/{id}.json", createStreamHandler(searchClient, conversionClient, redirectMap))
	r.HandleFunc("/redirect/{id}", createRedirectHandler(redirectMap))
	http.Handle("/", r)

	// CORS configuration
	headersOk := handlers.AllowedHeaders([]string{
		"Content-Type",
		"X-Requested-With",
		"Accept",
		"Accept-Language",
		"Accept-Encoding",
		"Content-Language",
		"Origin",
	})
	originsOk := handlers.AllowedOrigins([]string{"*"})
	methodsOk := handlers.AllowedMethods([]string{"GET"})

	// Timed logger for easier debugging with logs
	go func() {
		for {
			log.Println("...")
			time.Sleep(time.Second)
		}
	}()

	// Listen
	log.Println("Starting server")
	if err := http.ListenAndServe("0.0.0.0:8080", handlers.CORS(originsOk, headersOk, methodsOk)(r)); err != nil {
		log.Fatal("Couldn't start server:", err)
	}
}
