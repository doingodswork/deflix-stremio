package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/VictoriaMetrics/fastcache"
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

func createManifestHandler(ctx context.Context, conversionClient realdebrid.Client) http.HandlerFunc {
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

func createStreamHandler(ctx context.Context, searchClient imdb2torrent.Client, conversionClient realdebrid.Client, redirectCache *fastcache.Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("streamHandler called: %+v\n", r)
		rCtx := r.Context()

		params := mux.Vars(r)
		requestedType := params["type"]
		requestedID := params["id"]

		if requestedType != "movie" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		torrents, err := searchClient.FindMagnets(rCtx, requestedID)
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
		apiToken := rCtx.Value("apitoken").(string)
		availableInfoHashes := conversionClient.CheckInstantAvailability(rCtx, apiToken, infoHashes...)
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

		// Separate all torrent results into a 720p, 1080p, 1080p 10bit, 2160p and 2160p 10bit list, so we can offer the user one stream for each quality now (or maybe just for one quality if there's no torrent for the other), cache the torrents for each apiToken-imdbID-quality combination and later (at the redirect endpoint) go through the respective torrent list to turn in into a streamable video URL via RealDebrid.
		var torrents720p []imdb2torrent.Result
		var torrents1080p []imdb2torrent.Result
		var torrents1080p10bit []imdb2torrent.Result
		var torrents2160p []imdb2torrent.Result
		var torrents2160p10bit []imdb2torrent.Result
		for _, torrent := range torrents {
			if strings.HasPrefix(torrent.Quality, "720p") {
				torrents720p = append(torrents720p, torrent)
			} else if strings.HasPrefix(torrent.Quality, "1080p") && strings.Contains(torrent.Quality, "10bit") {
				torrents1080p10bit = append(torrents1080p10bit, torrent)
			} else if strings.HasPrefix(torrent.Quality, "1080p") {
				torrents1080p = append(torrents1080p, torrent)
			} else if strings.HasPrefix(torrent.Quality, "2160p") && strings.Contains(torrent.Quality, "10bit") {
				torrents2160p10bit = append(torrents2160p10bit, torrent)
			} else if strings.HasPrefix(torrent.Quality, "2160p") {
				torrents2160p = append(torrents2160p, torrent)
			} else {
				log.Println("Unknown quality, can't sort into one of the torrent lists:", torrent.Quality)
			}
		}

		// We already respond with two URLs (for both qualities, as long as we have two), but they point to our server for now.
		// Only when the user clicks on a stream and arrives at our redirect endpoint, we go through the list of torrents for the selected quality and try to convert them into a streamable video URL via RealDebrid.
		// There it should work for the first torrent we try, because we already checked the "instant availability" on RealDebrid here.
		var streams []stremio.StreamItem
		remote := false
		if remoteIface := rCtx.Value("remote"); remoteIface != nil {
			remote = remoteIface.(bool)
		}
		remoteString := strconv.FormatBool(remote)
		if len(torrents720p) > 0 {
			redirectID := apiToken + "-" + remoteString + "-" + requestedID + "-" + "720p"
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

			// Cache for upcoming redirect request
			if data, err := imdb2torrent.NewCacheEntry(rCtx, torrents720p); err != nil {
				log.Println("Couldn't create cache entry for torrent results:", err)
			} else {
				redirectCache.Set([]byte(redirectID), data)
			}
		}
		if len(torrents1080p) > 0 {
			redirectID := apiToken + "-" + remoteString + "-" + requestedID + "-" + "1080p"
			stream := stremio.StreamItem{
				URL:   "http://localhost:8080/redirect/" + redirectID,
				Title: "1080p",
			}
			if len(torrents1080p) == 1 {
				stream.Title = torrents1080p[0].Quality
			}
			streams = append(streams, stream)

			// Cache for upcoming redirect request
			if data, err := imdb2torrent.NewCacheEntry(rCtx, torrents1080p); err != nil {
				log.Println("Couldn't create cache entry for torrent results:", err)
			} else {
				redirectCache.Set([]byte(redirectID), data)
			}
		}
		if len(torrents1080p10bit) > 0 {
			redirectID := apiToken + "-" + remoteString + "-" + requestedID + "-" + "1080p-10bit"
			stream := stremio.StreamItem{
				URL:   "http://localhost:8080/redirect/" + redirectID,
				Title: "1080p 10bit",
			}
			if len(torrents1080p10bit) == 1 {
				stream.Title = torrents1080p10bit[0].Quality
			}
			streams = append(streams, stream)

			// Cache for upcoming redirect request
			if data, err := imdb2torrent.NewCacheEntry(rCtx, torrents1080p10bit); err != nil {
				log.Println("Couldn't create cache entry for torrent results:", err)
			} else {
				redirectCache.Set([]byte(redirectID), data)
			}
		}
		if len(torrents2160p) > 0 {
			redirectID := apiToken + "-" + remoteString + "-" + requestedID + "-" + "2160p"
			stream := stremio.StreamItem{
				URL:   "http://localhost:8080/redirect/" + redirectID,
				Title: "2160p",
			}
			if len(torrents2160p) == 1 {
				stream.Title = torrents2160p[0].Quality
			}
			streams = append(streams, stream)

			// Cache for upcoming redirect request
			if data, err := imdb2torrent.NewCacheEntry(rCtx, torrents2160p); err != nil {
				log.Println("Couldn't create cache entry for torrent results:", err)
			} else {
				redirectCache.Set([]byte(redirectID), data)
			}
		}
		if len(torrents2160p10bit) > 0 {
			redirectID := apiToken + "-" + remoteString + "-" + requestedID + "-" + "2160p-10bit"
			stream := stremio.StreamItem{
				URL:   "http://localhost:8080/redirect/" + redirectID,
				Title: "2160p 10bit",
			}
			if len(torrents2160p10bit) == 1 {
				stream.Title = torrents2160p10bit[0].Quality
			}
			streams = append(streams, stream)

			// Cache for upcoming redirect request
			if data, err := imdb2torrent.NewCacheEntry(rCtx, torrents2160p10bit); err != nil {
				log.Println("Couldn't create cache entry for torrent results:", err)
			} else {
				redirectCache.Set([]byte(redirectID), data)
			}
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

func createRedirectHandler(ctx context.Context, cache *fastcache.Cache, conversionClient realdebrid.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("redirectHandler called: %+v\n", r)
		rCtx := r.Context()

		params := mux.Vars(r)
		redirectID := params["id"]
		if redirectID == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		idParts := strings.Split(redirectID, "-")
		// "<apiToken>-<remote>-<imdbID>-<quality>"
		if len(idParts) != 4 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Check cache first.
		// Cache is important, because the video player (not Stremio!) sometimes calls this endpoint multiple times while waiting for the video stream to start!
		// Different from the other redirect cache (filled by the stream handler), eviction is important here, because this cache is not overwritten in the stream handler.
		// We only see "empty" cache entries as valid for 1 minute and full cache entries (for resuming paused streams for example) as valid for 24 hours.
		cacheKey := redirectID + "-stream"
		if streamURLgob, ok := cache.HasGet(nil, []byte(cacheKey)); ok {
			log.Println("Hit redirect cache for ID", redirectID)
			if streamURL, created, err := fromCacheEntry(rCtx, streamURLgob); err != nil {
				log.Println("Couldn't decode streamURL:", err)
			} else if len(streamURL) == 0 && time.Since(created) > time.Minute {
				log.Println("The torrents for this stream where previously tried to be converted into a stream but it didn't work. This was more than one minute ago though, so we'll try again.")
			} else if len(streamURL) == 0 {
				log.Println("The torrents for this stream where previously tried to be converted into a stream but it didn't work")
				w.WriteHeader(http.StatusNotFound)
				return
			} else if time.Since(created) > 24*time.Hour {
				log.Println("Found streamURL in cache, but expired since", time.Since(created.Add(24*time.Hour)))
			} else {
				log.Printf("Responding with redirect to: %s\n", streamURL)
				w.Header().Set("Location", string(streamURL))
				w.WriteHeader(http.StatusMovedPermanently)
				return
			}
		}

		// TODO: fastcache randomly removes cache entries when the cache grows bigger than its allowed size. This is ok for all typical cache usages, but here we *mis*use the cache as storage. The redirect entry *must* exist in the redirect handler after it was created by the stream handler.
		torrentsGob, ok := cache.HasGet(nil, []byte(redirectID))
		if !ok {
			log.Println("No torrents found for the redirect ID", redirectID)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// We ignore the cache entry creation date here, because the entry is not really *cached*, but we just (mis-)use the cache as size-limited storage and overwrite the value each time in the stream handler.
		// This also has the advantage that if a user pauses a stream and later continues it, the stream handler isn't used and no value is overwritten, but this redirect endpoint is called, with the above stream cache maybe being evicted, the following would still lead to a result after 24 hours.
		// *But* not sure how the player behaves when RealDebrid converts the torrents to a different stream URL (because for example the first torrent in the list isn't "instantly available" anymore) and the player seeks something like 5 minutes into the movie.
		torrentList, _, err := imdb2torrent.FromCacheEntry(rCtx, torrentsGob)
		if err != nil {
			log.Println("Couldn't decode torrent results:", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var streamURL string
		apiToken := idParts[0]
		remote, err := strconv.ParseBool(idParts[1])
		if err != nil {
			log.Println("Couldn't parse remote value", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, torrent := range torrentList {
			if streamURL, err = conversionClient.GetStreamURL(rCtx, torrent.MagnetURL, apiToken, remote); err != nil {
				log.Println("Couldn't get stream URL:", err)
			} else {
				break
			}
		}

		// Fill cache, even if no actual video stream was found, because it seems to be the current state on RealDebrid
		if streamURLgob, err := newCacheEntry(rCtx, streamURL); err != nil {
			log.Println("Couldn't encode streamURL:", err)
		} else {
			cache.Set([]byte(cacheKey), []byte(streamURLgob))
		}

		if streamURL == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		log.Printf("Responding with redirect to: %s\n", streamURL)
		w.Header().Set("Location", streamURL)
		w.WriteHeader(http.StatusMovedPermanently)
	}
}
