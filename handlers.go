package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
	"github.com/doingodswork/deflix-stremio/pkg/realdebrid"
	"github.com/doingodswork/deflix-stremio/pkg/stremio"
)

// The example code had this, but apparently it's not required and not used anywhere
// func homeHandler(w http.ResponseWriter, r *http.Request) {
// 	log.Printf("homeHandler called: %+v\n", r)
//
// 	w.Header().Set("Content-Type", "application/json")
// 	w.Write([]byte(`{"Path":"/"}`))
// }

func createManifestHandler(conversionClient realdebrid.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("manifestHandler called: %+v\n", r)

		params := mux.Vars(r)
		apiToken := params["apitoken"]
		if apiToken == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := conversionClient.TestToken(apiToken); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		resBody, _ := json.Marshal(manifest)
		log.Printf("Responding with: %s\n", resBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write(resBody)
	}
}

func catalogHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("catalogHandler called: %+v\n", r)

	params := mux.Vars(r)
	requestedType := params["type"]
	//requestedID := params["id"]

	// Currently movies only
	if requestedType != "movie" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	log.Printf("Responding with: %s\n", catalogResponse)
	w.Header().Set("Content-Type", "application/json")
	w.Write(catalogResponse)
}

func createStreamHandler(searchClient imdb2torrent.Client, conversionClient realdebrid.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("streamHandler called: %+v\n", r)

		params := mux.Vars(r)
		apiToken := params["apitoken"]
		requestedType := params["type"]
		requestedID := params["id"]

		if requestedType != "movie" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		if apiToken == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := conversionClient.TestToken(apiToken); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		// First check our catalog streams, because we prepared them
		stream, ok := catalogStreams[requestedID]
		if !ok {
			// Otherwise search for magnet URL on torrent sites
			res, err := searchClient.FindMagnet(requestedID)
			if err != nil {
				log.Println("Magnet not found:", err)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			stream = stremio.StreamItem{
				Title: res.Name,
				URL:   res.MagnetURL,
			}
		}

		// Turn magnet URL into debrid stream URL
		streamURL, err := conversionClient.GetStreamURL(stream.URL, apiToken)
		if err != nil {
			log.Println("Couldn't get stream URL:", err)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		stream.URL = streamURL

		streamJSON, _ := json.Marshal(stream)
		log.Printf(`Responding with: {"streams":[`+"%s]}\n", streamJSON)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams": [`))
		w.Write(streamJSON)
		w.Write([]byte(`]}`))
	}
}
