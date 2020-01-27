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

		ResourceItems: resources,
		Types:         []string{"movie"},
		// An empty slice is required for serializing to a JSON that Stremio expects
		Catalogs:      []stremio.CatalogItem{},

		IDprefixes: []string{"tt"},
		Background: "https://deflix.tv/images/Logo-1024px.png",
		Logo:       "https://deflix.tv/images/Logo-250px.png",
	}

	resources = []stremio.ResourceItem{
		stremio.ResourceItem{
			Name:  "stream",
			Types: []string{"movie"},
			// Not required as long as we define them globally in the manifest
			//IDprefixes: []string{"tt"},
		},
	}
)

func main() {
	// Timeout for global default HTTP client (for when using `http.Get()`)
	http.DefaultClient.Timeout = 5 * time.Second

	conversionClient := realdebrid.NewClient(5 * time.Second)
	searchClient := imdb2torrent.NewClient(5 * time.Second)

	log.Println("Setting up server")
	r := mux.NewRouter()
	r.HandleFunc("/{apitoken}/manifest.json", createManifestHandler(conversionClient))
	r.HandleFunc("/{apitoken}/stream/{type}/{id}.json", createStreamHandler(searchClient, conversionClient))
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
