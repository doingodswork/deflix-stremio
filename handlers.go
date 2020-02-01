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

var healthHandler = func(w http.ResponseWriter, r *http.Request) {
	if _, err := w.Write([]byte("OK")); err != nil {
		log.Println("Coldn't write response:", err)
	}
}

func createManifestHandler(conversionClient realdebrid.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("manifestHandler called: %+v\n", r)

		resBody, _ := json.Marshal(manifest)
		log.Printf("Responding with: %s\n", resBody)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(resBody); err != nil {
			log.Println("Coldn't write response:", err)
		}
	}
}

func createStreamHandler(searchClient imdb2torrent.Client, conversionClient realdebrid.Client, redirectMap map[string][]imdb2torrent.Result) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("streamHandler called: %+v\n", r)

		params := mux.Vars(r)
		requestedType := params["type"]
		requestedID := params["id"]

		if requestedType != "movie" {
			w.WriteHeader(http.StatusNotFound)
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
		apiToken := r.Context().Value("apitoken").(string)
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

		// Note: The torrents slice is guaranteed to not be empty at this point, because it already contained non-duplicate info hashes and then only unavailable ones were filtered and then a `len(availableInfoHashes) == 0` was done.

		// Separate all torrent results into one 720p and one 1080p list, so we can offer the user one stream for each quality now (or maybe just for one quality if there's no torrent for the other), cache the torrents for each apiToken-imdbID-quality combination and later (at the redirect endpoint) go through the respective torrent list to turn in into a streamable video URL via RealDebrid.
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

		// We already respond with two URLs (for both qualities, as long as we have two), but they point to our server for now.
		// Only when the user clicks on a stream and arrives at our redirect endpoint, we go through the list of torrents for the selected quality and try to convert them into a streamable video URL via RealDebrid.
		// There it should work for the first torrent we try, because we already checked the "instant availability" on RealDebrid here.
		var streams []stremio.StreamItem
		redirectID := apiToken + "-" + requestedID + "-"
		if len(torrents720p) > 0 {
			redirectID += "720p"
			stream := stremio.StreamItem{
				URL: streamURLaddr + "/redirect/" + redirectID,
				// Stremio docs recommend to use the stream quality as title.
				// See https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/stream.md#additional-properties-to-provide-information--behaviour-flags
				Title: "720p",
			}
			// We can only set the exact quality string if there's only one torrent.
			// Otherwise maybe the upcoming RealDebrid conversion fails for one torrent, but works for the next, which has a slightly different quality string.
			if len(torrents720p) == 1 {
				stream.Title = torrents720p[0].Quality
			}
			streams = append(streams, stream)
			redirectMap[redirectID] = torrents720p
		}
		if len(torrents1080p) > 0 {
			redirectID += "1080p"
			stream := stremio.StreamItem{
				URL:   "http://localhost:8080/redirect/" + redirectID,
				Title: "1080p",
			}
			if len(torrents1080p) == 1 {
				stream.Title = torrents1080p[0].Quality
			}
			streams = append(streams, stream)
			redirectMap[redirectID] = torrents1080p
		}

		streamJSON, _ := json.Marshal(streams)
		log.Printf(`Responding with: {"streams":`+"%s}\n", streamJSON)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"streams": `)); err != nil {
			log.Println("Coldn't write response:", err)
		} else if _, err = w.Write(streamJSON); err != nil {
			log.Println("Coldn't write response:", err)
		} else if _, err = w.Write([]byte(`}`)); err != nil {
			log.Println("Coldn't write response:", err)
		}
	}
}

func createRedirectHandler(redirectMap map[string][]imdb2torrent.Result, conversionClient realdebrid.Client) http.HandlerFunc {
	cache := make(map[string]string)
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("redirectHandler called: %+v\n", r)

		params := mux.Vars(r)
		redirectID := params["id"]
		if redirectID == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Check cache first.
		// Cache is important, because somehow Stremio sometimes calls this endpoint multiple times while waiting for the video stream to start!
		if streamURL, ok := cache[redirectID]; ok {
			log.Printf("Hit redirect cache. ID: %v; URL: %v\n", redirectID, streamURL)
			log.Printf("Responding with redirect to: %s\n", streamURL)
			w.Header().Set("Location", streamURL)
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}

		torrentList, ok := redirectMap[redirectID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		idParts := strings.Split(redirectID, "-")
		// "<apiToken>-<imdbID>-<quality>"
		if len(idParts) != 3 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var streamURL string
		var err error
		apiToken := idParts[0]
		for _, torrent := range torrentList {
			if streamURL, err = conversionClient.GetStreamURL(torrent.MagnetURL, apiToken); err != nil {
				log.Println("Couldn't get stream URL:", err)
			} else {
				break
			}
		}

		// Fill cache, even if no actual video stream was found, because it seems to be the current state on RealDebrid
		cache[redirectID] = streamURL

		if streamURL == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		log.Printf("Responding with redirect to: %s\n", streamURL)
		w.Header().Set("Location", streamURL)
		w.WriteHeader(http.StatusMovedPermanently)
	}
}
