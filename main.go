package main

import (
	"bytes"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/tidwall/gjson"

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
		Catalogs:      catalogs,

		IDprefixes: []string{"tt"},
		Background: "https://deflix.tv/images/Logo-1024px.png",
		Logo:       "https://deflix.tv/images/Logo-250px.png",
	}

	resources = []stremio.ResourceItem{
		stremio.ResourceItem{
			Name: "catalog",
		},
		stremio.ResourceItem{
			Name:  "stream",
			Types: []string{"movie"},
			// Not required as long as we define them globally in the manifest
			//IDprefixes: []string{"tt"},
		},
	}

	catalogs = []stremio.CatalogItem{
		stremio.CatalogItem{
			Type: "movie",
			ID:   "libre",
			Name: "Some free and legal torrents"},
	}

	catalogStreams = map[string]stremio.StreamItem{
		"tt1254207": stremio.StreamItem{
			Title: "Big Buck Bunny",
			URL:   "magnet:?xt=urn:btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&dn=Big+Buck+Bunny&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.fastcast.nz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com&ws=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2F&xs=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2Fbig-buck-bunny.torrent"},
		"tt1727587": stremio.StreamItem{
			Title: "Sintel",
			URL:   "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.fastcast.nz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com&ws=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2F&xs=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2Fsintel.torrent"},
	}

	// Cached JSON from Cinemata addon response
	catalogResponse []byte
)

func main() {
	// Timeout for global default HTTP client (for when using `http.Get()`)
	http.DefaultClient.Timeout = 5 * time.Second

	log.Println("Initializing catalog...")
	initializeCatalog()
	log.Println("Finished initializing catalog")

	conversionClient := realdebrid.NewClient(5 * time.Second)
	searchClient := imdb2torrent.NewClient(5 * time.Second)

	log.Println("Setting up server")
	r := mux.NewRouter()
	r.HandleFunc("/{apitoken}/manifest.json", createManifestHandler(conversionClient))
	r.HandleFunc("/{apitoken}/stream/{type}/{id}.json", createStreamHandler(searchClient, conversionClient))
	r.HandleFunc("/{apitoken}/catalog/{type}/{id}.json", catalogHandler)
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

func initializeCatalog() {
	// Use when adding single movie infos
	//buf := bytes.NewBufferString(`{"metas":[`)
	buf := bytes.NewBufferString(`{"metas":`)

	// Alternative for single movie infos:
	// "https://v3-cinemeta.strem.io/meta/movie/" + imdbID + ".json"
	url := "https://v3-cinemeta.strem.io/catalog/movie/last-videos/lastVideosIds="
	for imdbID := range catalogStreams {
		url += imdbID + ","
	}
	url = strings.TrimSuffix(url, ",")
	url += ".json"
	// Add "?sda" to invalidate the server's cache

	res, err := http.Get(url)
	if err != nil {
		log.Println("Couldn't GET", url)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		log.Println("Bad GET response:", res.StatusCode)
	}
	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Println("Couldn't read response body:", err)
	}

	meta := gjson.GetBytes(resBody, "metasDetailed").String()
	buf.WriteString(meta)
	// Use when adding single movie infos
	//buf.WriteString("]}")
	buf.WriteString("}")

	catalogResponse = buf.Bytes()
}
