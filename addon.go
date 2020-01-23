package main

import (
	//"fmt"
	"encoding/json"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"log"
	"net/http"
	"strings"
)

var CATALOG_ID = "Hello, Go"

var MANIFEST = Manifest{
	Id:          "org.stremio.helloworld.go",
	Version:     "0.0.1",
	Name:        "Hello World Go Addon",
	Description: "Sample addon made with gorilla/mux package providing a few public domain movies",
	Types:       []string{"movie", "series"},
	Catalogs:    []CatalogItem{},
	Resources:   []string{"stream", "catalog"},
}

var movieMap map[string]StreamItem
var seriesMap map[string]StreamItem

var movieMetaMap map[string]MetaItem
var seriesMetaMap map[string]MetaItem

var METAHUB_BASE_URL = "https://images.metahub.space/poster/medium/"

func initializeMaps() {
	movieMap = make(map[string]StreamItem)
	seriesMap = make(map[string]StreamItem)

	// Movies
	movieMap["tt0051744"] = StreamItem{Title: "House on Haunted Hill",
		InfoHash: "9f86563ce2ed86bbfedd5d3e9f4e55aedd660960"}
	movieMap["tt1254207"] = StreamItem{Title: "Big Buck Bunny",
		Url: "http://clips.vorwaerts-gmbh.de/big_buck_bunny.mp4"}
	movieMap["tt0031051"] = StreamItem{Title: "The Arizona Kid", YtId: "m3BKVSpP80s"}
	movieMap["tt0137523"] = StreamItem{Title: "Fight Club",
		ExternalUrl: "https://www.netflix.com/watch/26004747"}

	//Series
	seriesMap["tt1748166"] = StreamItem{Title: "Pioneer One",
		InfoHash: "07a9de9750158471c3302e4e95edb1107f980fa6"}

	// Meta
	movieMetaMap = make(map[string]MetaItem)
	seriesMetaMap = make(map[string]MetaItem)

	movieMetaMap["tt0051744"] = MetaItem{Name: "House on Haunted Hill",
		Genres: []string{"Horror", "Mystery"}}
	movieMetaMap["tt1254207"] = MetaItem{Name: "Big Buck Bunny", Genres: []string{"Animation", "Short", "Comedy"},
		Poster: "https://peach.blender.org/wp-content/uploads/poster_bunny_small.jpg"}
	movieMetaMap["tt0031051"] = MetaItem{Name: "The Arizona Kid",
		Genres: []string{"Music", "War", "Western"}}
	movieMetaMap["tt0137523"] = MetaItem{Name: "Fight Club",
		Genres: []string{"Drama"}}

	//Series
	seriesMetaMap["tt1748166"] = MetaItem{Name: "Pioneer One",
		Genres: []string{"Drama"}}
}

func main() {
	initializeMaps()

	MANIFEST.Catalogs = append(MANIFEST.Catalogs, CatalogItem{"movie", CATALOG_ID})
	MANIFEST.Catalogs = append(MANIFEST.Catalogs, CatalogItem{"series", CATALOG_ID})

	r := mux.NewRouter()
	r.HandleFunc("/", HomeHandler)
	r.HandleFunc("/manifest.json", ManifestHandler)
	r.HandleFunc("/stream/{type}/{id}.json", StreamHandler)
	r.HandleFunc("/catalog/{type}/{id}.json", CatalogHandler)
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

	// Listen
	err := http.ListenAndServe("0.0.0.0:3593", handlers.CORS(originsOk, headersOk, methodsOk)(r))
	if err != nil {
		log.Fatalf("Listen: %s", err.Error())
	}
}

func HomeHandler(w http.ResponseWriter, r *http.Request) {
	jr, _ := json.Marshal(jsonObj{"Path": '/'})
	w.Header().Set("Content-Type", "application/json")
	w.Write(jr)
}

func ManifestHandler(w http.ResponseWriter, r *http.Request) {
	jr, _ := json.Marshal(MANIFEST)
	w.Header().Set("Content-Type", "application/json")
	w.Write(jr)
}

func StreamHandler(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	stream := StreamItem{}

	if params["type"] == "movie" {
		stream = movieMap[params["id"]]
	} else if params["type"] == "series" {
		itemIds := strings.Split(params["id"], ":")
		showID, seasonId, episodeId := itemIds[0], itemIds[1], itemIds[2]
		stream = seriesMap[showID] // XXX: season, episode
		// silence the compiler
		if seasonId+episodeId != string(stream.FileIdx) {
			log.Println("Return stream for episode 1")
		}
	} else {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"streams": [`))
	streamJson, _ := json.Marshal(stream)
	w.Write(streamJson)
	w.Write([]byte(`]}`))
}

func CatalogHandler(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	metaMap := make(map[string]MetaItem)

	for _, item := range MANIFEST.Catalogs {
		if params["id"] == item.Id && params["type"] == item.Type {
			switch item.Type {
			case "series":
				metaMap = seriesMetaMap
			case "movie":
				metaMap = movieMetaMap
			default:
				continue
			}
			break
		}
	}

	if len(metaMap) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	metas := []MetaItemJson{}
	for metaKey, metaValue := range metaMap {
		item := MetaItemJson{
			Id:     metaKey,
			Type:   params["type"],
			Name:   metaValue.Name,
			Genres: metaValue.Genres,
			Poster: METAHUB_BASE_URL + metaKey + "/img",
		}
		if metaValue.Poster != "" {
			item.Poster = metaValue.Poster
		}
		metas = append(metas, item)
	}

	w.Header().Set("Content-Type", "application/json")
	catalogJson, _ := json.Marshal(&jsonObj{"metas": metas})
	w.Write(catalogJson)
}
