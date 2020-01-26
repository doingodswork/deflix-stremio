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

		potentialStreams := []stremio.StreamItem{}
		// First check our catalog streams, because we prepared them
		if stream, ok := catalogStreams[requestedID]; ok {
			potentialStreams = append(potentialStreams, stream)
		} else {
			// Otherwise search for magnet URL on torrent sites
			results, err := searchClient.FindMagnets(requestedID)
			if err != nil {
				log.Println("Magnet not found:", err)
				w.WriteHeader(http.StatusNotFound)
				return
			} else if len(results) == 0 {
				log.Println("No magnets found")
				w.WriteHeader(http.StatusNotFound)
				return
			}
			for _, result := range results {
				stream = stremio.StreamItem{
					// Stremio docs recommend to use the stream quality as title.
					// See https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/stream.md#additional-properties-to-provide-information--behaviour-flags
					Title: result.Quality,
					URL:   result.MagnetURL,
				}
				potentialStreams = append(potentialStreams, stream)
			}
		}

		// Turn magnet URL into debrid stream URL
		actualStreams := []stremio.StreamItem{}
		for _, stream := range potentialStreams {
			streamURL, err := conversionClient.GetStreamURL(stream.URL, apiToken)
			if err != nil {
				log.Println("Couldn't get stream URL:", err)
			} else {
				stream.URL = streamURL
				actualStreams = append(actualStreams, stream)
			}
		}

		streamJSON, _ := json.Marshal(actualStreams)
		log.Printf(`Responding with: {"streams":`+"%s}\n", streamJSON)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams": `))
		w.Write(streamJSON)
		w.Write([]byte(`}`))
	}
}
