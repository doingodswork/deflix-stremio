package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/gorilla/mux"
	gocache "github.com/patrickmn/go-cache"
	log "github.com/sirupsen/logrus"

	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
	"github.com/doingodswork/deflix-stremio/pkg/realdebrid"
	"github.com/doingodswork/deflix-stremio/pkg/stremio"
)

const (
	bigBuckBunnyMagnet = `magnet:?xt=urn:btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&dn=Big+Buck+Bunny&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.fastcast.nz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com&ws=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2F&xs=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2Fbig-buck-bunny.torrent`
)

// The example code had this, but apparently it's not required and not used anywhere
// func homeHandler(w http.ResponseWriter, r *http.Request) {
// 	log.Printf("homeHandler called: %+v\n", r)
//
// 	w.Header().Set("Content-Type", "application/json")
// 	w.Write([]byte(`{"Path":"/"}`))
// }

var healthHandler = func(w http.ResponseWriter, r *http.Request) {
	rCtx := r.Context()
	logger := log.WithContext(rCtx)
	logger.WithField("request", r).Trace("healthHandler called")

	if _, err := w.Write([]byte("OK")); err != nil {
		logger.WithError(err).Error("Coldn't write response")
	}
}

func createManifestHandler(ctx context.Context, conversionClient realdebrid.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rCtx := r.Context()
		logger := log.WithContext(rCtx)
		logger.WithField("request", r).Trace("manifestHandler called")

		resBody, _ := json.Marshal(manifest)
		logger.Debugf("Responding with: %s\n", resBody)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(resBody); err != nil {
			logger.WithError(err).Error("Coldn't write response")
		}
	}
}

func createStreamHandler(ctx context.Context, config config, searchClient imdb2torrent.Client, conversionClient realdebrid.Client, redirectCache *gocache.Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rCtx := r.Context()
		logger := log.WithContext(rCtx)
		logger.WithField("request", r).Trace("streamHandler called")

		params := mux.Vars(r)
		requestedType := params["type"]
		requestedID := params["id"]

		if requestedType != "movie" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		torrents, err := searchClient.FindMagnets(rCtx, requestedID)
		if err != nil {
			logger.WithError(err).Warn("Magnet not found")
			w.WriteHeader(http.StatusNotFound)
			return
		} else if len(torrents) == 0 {
			logger.Info("No magnets found")
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
			logger.Info("None of the found torrents are instantly available on real-debrid.com")
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
				logger.WithField("quality", torrent.Quality).Warn("Unknown quality, can't sort into one of the torrent lists")
			}
		}

		// Cache results to make this data available in the redirect handler. It will pick the first torrent from the list and convert it via RD, or pick the next if the previous didn't work.
		// There's no need to cache this for a specific user.
		// This cache *must* be a cache where items aren't evicted when the cache is full, because otherwise if the cache is full and two users fetch available streams, then the second one could lead to the first cache item being evicted before the first user clicks on the stream, leading to an error inside the redirect handler after he clicks on the stream.
		redirectCache.Set(requestedID+"-720p", torrents720p, 0)
		redirectCache.Set(requestedID+"-1080p", torrents1080p, 0)
		redirectCache.Set(requestedID+"-1080p-10bit", torrents1080p10bit, 0)
		redirectCache.Set(requestedID+"-2160p", torrents2160p, 0)
		redirectCache.Set(requestedID+"-2160p-10bit", torrents2160p10bit, 0)

		// We already respond with several URLs (one for each quality, as long as we have torrents for the different qualities), but they point to our server for now.
		// Only when the user clicks on a stream and arrives at our redirect endpoint, we go through the list of torrents for the selected quality and try to convert them into a streamable video URL via RealDebrid.
		// There it should usually work for the first torrent we try, because we already checked the "instant availability" on RealDebrid here. If the "instant availability" info is stale (because we cached it), the next torrent will be used.
		var streams []stremio.StreamItem
		remote := false
		if remoteIface := rCtx.Value("remote"); remoteIface != nil {
			remote = remoteIface.(bool)
		}
		remoteString := strconv.FormatBool(remote)
		requestIDPrefix := apiToken + "-" + remoteString + "-" + requestedID
		if len(torrents720p) > 0 {
			stream := createStreamItem(rCtx, config, requestIDPrefix+"-"+"720p", "720p", torrents720p)
			streams = append(streams, stream)
		}
		if len(torrents1080p) > 0 {
			stream := createStreamItem(rCtx, config, requestIDPrefix+"-"+"1080p", "1080p", torrents1080p)
			streams = append(streams, stream)
		}
		if len(torrents1080p10bit) > 0 {
			stream := createStreamItem(rCtx, config, requestIDPrefix+"-"+"1080p-10bit", "1080p 10bit", torrents1080p10bit)
			streams = append(streams, stream)
		}
		if len(torrents2160p) > 0 {
			stream := createStreamItem(rCtx, config, requestIDPrefix+"-"+"2160p", "2160p", torrents2160p)
			streams = append(streams, stream)
		}
		if len(torrents2160p10bit) > 0 {
			stream := createStreamItem(rCtx, config, requestIDPrefix+"-"+"2160p-10bit", "2160p 10bit", torrents2160p10bit)
			streams = append(streams, stream)
		}

		streamJSON, _ := json.Marshal(streams)
		logger.WithField("response", fmt.Sprintf(`{"streams": %s}`, streamJSON)).Debug("Responding")
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"streams": `)); err != nil {
			logger.WithError(err).Error("Coldn't write response")
		} else if _, err = w.Write(streamJSON); err != nil {
			logger.WithError(err).Error("Coldn't write response")
		} else if _, err = w.Write([]byte(`}`)); err != nil {
			logger.WithError(err).Error("Coldn't write response")
		}
	}
}

func createStreamItem(ctx context.Context, config config, redirectID, quality string, torrents []imdb2torrent.Result) stremio.StreamItem {
	stream := stremio.StreamItem{
		URL: config.StreamURLaddr + "/redirect/" + redirectID,
		// Stremio docs recommend to use the stream quality as title.
		// See https://github.com/Stremio/stremio-addon-sdk/blob/ddaa3b80def8a44e553349734dd02ec9c3fea52c/docs/api/responses/stream.md#additional-properties-to-provide-information--behaviour-flags
		Title: quality,
	}
	// We can only set the exact quality string if there's only one torrent.
	// Otherwise maybe the upcoming RealDebrid conversion fails for one torrent, but works for the next, which has a slightly different quality string.
	if len(torrents) == 1 {
		stream.Title = torrents[0].Quality
	}

	// Create and assign lock object.
	// Note: A lock object might exist already from a previous stream handler call, or even after a service restart when a user first resumed a movie (and so called the redirect handler first) before calling the stream handler for the same movie again.
	redirectLockMapLock.Lock()
	defer redirectLockMapLock.Unlock()
	if _, ok := redirectLock[redirectID]; !ok {
		redirectLock[redirectID] = &sync.Mutex{}
	}

	return stream
}

func createRedirectHandler(ctx context.Context, redirectCache *gocache.Cache, conversionClient realdebrid.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rCtx := r.Context()
		logger := log.WithContext(rCtx)
		logger.WithField("request", r).Trace("redirectHandler called")

		params := mux.Vars(r)
		redirectID := params["id"]
		if redirectID == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		logger = logger.WithField("redirectID", redirectID)

		idParts := strings.Split(redirectID, "-")
		// "<apiToken>-<remote>-<imdbID>-<quality>"
		if len(idParts) != 4 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		apiToken := idParts[0]
		remote, err := strconv.ParseBool(idParts[1])
		if err != nil {
			logger.WithError(err).Error("Couldn't parse remote value")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		imdbID := idParts[2]
		quality := idParts[3]

		// Before we look into the cache, we need to set a lock so that concurrent calls to this endpoint (including the redirectID) don't unnecessarily lead to the full sharade of RD requests again, only because the first handling of the request wasn't fast enough to fill the cache.
		// The lock objects are created in the stream handler. But if the service was restarted the map is empty. So we need to create lock objects in that case for the users arriving at the redirect handler without having been at the stream handler after a service restart.
		redirectLockMapLock.Lock()
		if _, ok := redirectLock[redirectID]; !ok {
			redirectLock[redirectID] = &sync.Mutex{}
		}
		redirectLockMapLock.Unlock()
		redirectLock[redirectID].Lock()
		defer redirectLock[redirectID].Unlock()

		// Check stream cache first.
		// Here we don't get the data that's passed from the stream handler to this redirect handler, but instead the the RealDebrid HTTP stream URL, which is cached after it was converted in a previous call.
		// This cache is important, because for a single click on a stream in Stremio there are multiple requests to this endpoint in a short timeframe.
		// This cache is also useful for when a user resumes his stream via Stremio after closing it. In this case the same RealDebrid HTTP stream must be delivered (or even if it would work with another one, using the same one would be beneficial).
		// TODO: We don't know how long RealDebrid HTTP stream URLs are valid though! If it's shorter, we can shorten this as well. Also see similar TODO comment in main.go file.
		if streamURLiface, found := streamCache.Get(redirectID); found {
			logger.Debug("Hit stream cache")
			if streamURLitem, ok := streamURLiface.(cacheItem); !ok {
				logger.WithField("cacheItemType", fmt.Sprintf("%T", streamURLiface)).Error("Stream cache item couldn't be cast into cacheItem")
			} else if len(streamURLitem.Value) == 0 && time.Since(streamURLitem.Created) > time.Minute {
				logger.Warn("The torrents for this stream where previously tried to be converted into a stream but it didn't work. This was more than one minute ago though, so we'll try again.")
			} else if len(streamURLitem.Value) == 0 {
				logger.Warn("The torrents for this stream where previously tried to be converted into a stream but it didn't work")
				w.WriteHeader(http.StatusNotFound)
				return
			} else {
				logger.WithField("redirectLocation", streamURLitem.Value).Debug("Responding with redirect to stream")
				w.Header().Set("Location", streamURLitem.Value)
				w.WriteHeader(http.StatusMovedPermanently)
				return
			}
		}

		// Here we get the data from the cache that the stream handler filled.
		torrentsIface, found := redirectCache.Get(imdbID + "-" + quality)
		if !found {
			logger.Warnf("No torrents cache item found for %v, did 24h pass?", imdbID+"-"+quality)
			// TODO: Just run the same stuff the stream handler does! This way we can drastically reduce the required cache time for the redirect cache, and the scraping doesn't really take long! Take care of concurrent requests - maybe lock!
			w.WriteHeader(http.StatusNotFound)
			return
		}
		torrents, ok := torrentsIface.([]imdb2torrent.Result)
		if !ok {
			logger.WithField("cacheItemType", fmt.Sprintf("%T", torrentsIface)).Error("Torrents cache item couldn't be cast into []imdb2torrent.Result")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var streamURL string
		for _, torrent := range torrents {
			if streamURL, err = conversionClient.GetStreamURL(rCtx, torrent.MagnetURL, apiToken, remote); err != nil {
				logger.WithError(err).Warn("Couldn't get stream URL")
			} else {
				break
			}
		}

		// Fill cache, even if no actual video stream was found, because it seems to be the current state on RealDebrid
		streamURLitem := cacheItem{
			Value:   streamURL,
			Created: time.Now(),
		}
		streamCache.Set(redirectID, streamURLitem, 0)

		if streamURL == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		logger.WithField("redirectLocation", streamURL).Debug("Responding with redirect to stream")
		w.Header().Set("Location", streamURL)
		w.WriteHeader(http.StatusMovedPermanently)
	}
}

func createRootHandler(ctx context.Context, config config) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		rCtx := r.Context()
		logger := log.WithContext(rCtx)
		logger.WithField("request", r).Trace("rootHandler called")

		logger.WithField("redirectLocation", config.RootURL).Debug("Responding with redirect")
		w.Header().Set("Location", config.RootURL)
		w.WriteHeader(http.StatusMovedPermanently)
	}
}

func createStatusHandler(mainCtx context.Context, magnetSearchers map[string]imdb2torrent.MagnetSearcher, conversionClient realdebrid.Client, fastCaches map[string]*fastcache.Cache, goCaches map[string]*gocache.Cache) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		rCtx := r.Context()
		logger := log.WithContext(rCtx)
		logger.WithField("request", r).Trace("statusHandler called")

		queryVals := r.URL.Query()
		imdbID := queryVals.Get("imdbid")
		apiToken := queryVals.Get("apitoken")
		if imdbID == "" || apiToken == "" {
			logger.Warn("\"/status\" was called without IMDb ID or API token")
			w.WriteHeader(http.StatusBadRequest)
		}

		start := time.Now()
		res := "{\n"

		// Check magnet searchers

		res += "\t" + `"magnetSearchers": {` + "\n"
		// Lock for writing to the same string
		lock := sync.Mutex{}
		wg := sync.WaitGroup{}
		wg.Add(len(magnetSearchers))
		for name, client := range magnetSearchers {
			go func(goName string, goClient imdb2torrent.MagnetSearcher) {
				defer wg.Done()
				if goClient.IsSlow() {
					res += "\t\t" + `"` + goName + `": "quick skip",` + "\n"
					return
				}
				startSearch := time.Now()
				results, err := goClient.Find(rCtx, imdbID)
				lock.Lock()
				defer lock.Unlock()
				res += "\t\t" + `"` + goName + `": {` + "\n"
				if err != nil {
					res += "\t\t\t" + `"err":"` + err.Error() + `",` + "\n"
				} else {
					resCount := len(results)
					res += "\t\t\t" + `"resCount":"` + strconv.Itoa(resCount) + `",` + "\n"
					if resCount > 0 {
						resExample := fmt.Sprintf("%+v", results[0])
						resExample = strings.ReplaceAll(resExample, "\n", " ")
						res += "\t\t\t" + `"resExample":"` + resExample + `",` + "\n"
					}
				}
				durationSearchmillis := time.Since(startSearch).Milliseconds()
				res += "\t\t\t" + `"duration": "` + strconv.FormatInt(durationSearchmillis, 10) + `ms"` + "\n"
				res += "\t\t" + `},` + "\n"
			}(name, client)
		}
		wg.Wait()
		res = strings.TrimRight(res, ",\n") + "\n"
		res += "\t" + `},` + "\n"

		// Check RD client

		res += "\t" + `"RD": {` + "\n"
		startRD := time.Now()
		streamURL, err := conversionClient.GetStreamURL(rCtx, bigBuckBunnyMagnet, apiToken, false)
		if err != nil {
			res += "\t\t" + `"err":"` + err.Error() + `",` + "\n"
		} else {
			res += "\t\t" + `"res":"` + streamURL + `",` + "\n"
		}
		durationRDmillis := time.Since(startRD).Milliseconds()
		res += "\t\t" + `"duration": "` + strconv.FormatInt(durationRDmillis, 10) + `ms"` + "\n"
		res += "\t" + `},` + "\n"

		// Check caches

		res += "\t" + `"caches": {` + "\n"
		stats := fastcache.Stats{}
		for name, cache := range fastCaches {
			res += "\t\t" + `"` + name + `": {` + "\n"
			cache.UpdateStats(&stats)
			res += "\t\t\t" + `"GetCalls": "` + strconv.FormatUint(stats.GetCalls, 10) + `"` + ",\n"
			res += "\t\t\t" + `"SetCalls": "` + strconv.FormatUint(stats.SetCalls, 10) + `"` + ",\n"
			res += "\t\t\t" + `"Misses": "` + strconv.FormatUint(stats.Misses, 10) + `"` + ",\n"
			res += "\t\t\t" + `"Collisions": "` + strconv.FormatUint(stats.Collisions, 10) + `"` + ",\n"
			res += "\t\t\t" + `"Corruptions": "` + strconv.FormatUint(stats.Corruptions, 10) + `"` + ",\n"
			res += "\t\t\t" + `"EntriesCount": "` + strconv.FormatUint(stats.EntriesCount, 10) + `"` + ",\n"
			res += "\t\t\t" + `"Size": "` + strconv.FormatUint(stats.BytesSize/uint64(1024)/uint64(1024), 10) + "MB" + `"` + "\n"
			res += "\t\t" + `},` + "\n"
			stats.Reset()
		}
		for name, cache := range goCaches {
			res += "\t\t" + `"` + name + `": {` + "\n"
			res += "\t\t\t" + `"Items": "` + strconv.Itoa(cache.ItemCount()) + `"` + ",\n"
			res += "\t\t" + `},` + "\n"
		}
		res = strings.TrimRight(res, ",\n") + "\n"
		res += "\t" + `},` + "\n"

		durationMillis := time.Since(start).Milliseconds()
		res += "\t" + `"duration": "` + strconv.FormatInt(durationMillis, 10) + `ms"` + "\n"
		res += "}"

		logger.WithField("response", res).Debug("Responding")
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(res)); err != nil {
			logger.WithError(err).Error("Couldn't write response")
		}
	}
}
