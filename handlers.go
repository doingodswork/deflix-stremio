package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

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

		torrents, err := searchClient.FindMagnets(requestedID)
		if err != nil {
			log.Println("Magnet not found:", err)
			w.WriteHeader(http.StatusNotFound)
			return
		} else if len(torrents) == 0 {
			log.Println("No magnets found")
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Filter out the ones that are not available
		var infoHashes []string
		for _, torrent := range torrents {
			infoHashes = append(infoHashes, torrent.InfoHash)
		}
		availableInfoHashes := conversionClient.CheckInstantAvailability(apiToken, infoHashes...)
		if len(availableInfoHashes) == 0 {
			// TODO: queue for download on real-debrid, or log somewhere for an asynchronous process to go through them and queue them?
			log.Println("None of the found torrents are instantly available on real-debrid.com")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// https://github.com/golang/go/wiki/SliceTricks#filter-in-place
		n := 0
		for _, torrent := range torrents {
			for _, availableInfoHash := range availableInfoHashes {
				if torrent.InfoHash == availableInfoHash {
					torrents[n] = torrent
					n++
					break
				}
			}
		}
		torrents = torrents[:n]

		// Turn torrents into streams.
		// Only keep *one* 720p and *one* 1080p stream.
		// The streams should already be roughtly ordered by the quality of their source (e.g. YTS on top), so we can skip as soon as we have one of each.
		//
		// We want to parallelize the requests, but also only want to make as few requests as possible (one successful for 720p, one successful for 1080p).
		// Going through the full list in parallel could lead for example to two successful 720p requests.
		// Solution: Make separate lists for 720p and 1080p, go through both lists in parallel, but sequentially *per* list.
		var torrents720p []imdb2torrent.Result
		var torrents1080p []imdb2torrent.Result
		for _, torrent := range torrents {
			// TODO: If we know 100% the quality starts with the searched string, strings.HasPrefix() might be faster.
			if strings.Contains(torrent.Quality, "720p") {
				torrents720p = append(torrents720p, torrent)
			} else {
				torrents1080p = append(torrents1080p, torrent)
			}
		}

		streamChan := make(chan stremio.StreamItem, 2)
		for _, torrentList := range [][]imdb2torrent.Result{torrents720p, torrents1080p} {
			go func(goroutineTorrentList []imdb2torrent.Result) {
				for _, torrent := range goroutineTorrentList {
					streamURL, err := conversionClient.GetStreamURL(torrent.MagnetURL, apiToken)
					if err != nil {
						log.Println("Couldn't get stream URL:", err)
						streamChan <- stremio.StreamItem{}
					} else {
						streamChan <- stremio.StreamItem{
							// Stremio docs recommend to use the stream quality as title.
							// See https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/stream.md#additional-properties-to-provide-information--behaviour-flags
							Title: torrent.Quality,
							URL:   streamURL,
						}
						// Stop the goroutine!
						return
					}
				}
			}(torrentList)
		}

		var streams []stremio.StreamItem
		for i := 0; i < 2; i++ {
			stream := <-streamChan
			if stream.URL != "" {
				streams = append(streams, stream)
			}
		}
		close(streamChan)

		streamJSON, _ := json.Marshal(streams)
		log.Printf(`Responding with: {"streams":`+"%s}\n", streamJSON)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams": `))
		w.Write(streamJSON)
		w.Write([]byte(`}`))
	}
}
