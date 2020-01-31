package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

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

func createStreamHandler(searchClient imdb2torrent.Client, conversionClient realdebrid.Client, redirectMap map[string]string) http.HandlerFunc {
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

		// We already respond with two URLs (for both qualities, as long as we have two), but they point to our server for now.
		// After the response we will process the torrents on RealDebrid *in the background* and fill the redirectMap with the results!
		// When the user clicks on a stream, we should have the final streamable video URL from RealDebrid by then and can *redirect* to it.
		// This is important because it takes quite a while and we don't want to let the user wait so long for the stream buttons to appear.
		var rand720p string
		var rand1080p string
		var streams []stremio.StreamItem
		if len(torrents720p) > 0 {
			rand720p = randString(32)
			stream := stremio.StreamItem{
				URL: "http://localhost:8080/redirect/" + rand720p,
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
		}
		if len(torrents1080p) > 0 {
			rand1080p = randString(32)
			stream := stremio.StreamItem{
				URL:   "http://localhost:8080/redirect/" + rand1080p,
				Title: "1080p",
			}
			if len(torrents720p) == 1 {
				stream.Title = torrents1080p[0].Quality
			}
			streams = append(streams, stream)
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

		// Now process the torrents on RealDebrid in the background
		for listNo, torrentList := range [][]imdb2torrent.Result{torrents720p, torrents1080p} {
			go func(goroutineListNo int, goroutineTorrentList []imdb2torrent.Result) {
				for _, torrent := range goroutineTorrentList {
					streamURL, err := conversionClient.GetStreamURL(torrent.MagnetURL, apiToken)
					if err != nil {
						log.Println("Couldn't get stream URL:", err)
					} else if goroutineListNo == 0 {
						redirectMap[rand720p] = streamURL
						// Stop the goroutine!
						return
					} else if goroutineListNo == 1 {
						redirectMap[rand1080p] = streamURL
						return
					}
				}
			}(listNo, torrentList)
		}
	}
}

func createRedirectHandler(redirectMap map[string]string) http.HandlerFunc {
	rdRequestCount := 5
	rdRequestTimeout := 3
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("redirectHandler called: %+v\n", r)

		params := mux.Vars(r)
		redirectID := params["id"]
		if redirectID == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		streamURL, ok := redirectMap[redirectID]
		// Could be that the background job is still running, let's wait a bit and try multiple times
		waitSeconds := time.Duration(rdRequestCount*rdRequestTimeout) * time.Second
		for !ok && waitSeconds > 0 {
			time.Sleep(time.Second)
			waitSeconds--
			streamURL, ok = redirectMap[redirectID]
		}

		log.Printf("Responding with redirect to: %s\n", streamURL)
		w.Header().Set("Location", streamURL)
		w.WriteHeader(http.StatusMovedPermanently)
	}
}
