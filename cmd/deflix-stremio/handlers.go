package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber"
	gocache "github.com/patrickmn/go-cache"
	"go.uber.org/zap"

	"github.com/deflix-tv/go-stremio"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/alldebrid"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/realdebrid"
	"github.com/doingodswork/deflix-stremio/pkg/imdb2torrent"
)

const (
	bigBuckBunnyMagnet = `magnet:?xt=urn:btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&dn=Big+Buck+Bunny&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.fastcast.nz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com&ws=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2F&xs=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2Fbig-buck-bunny.torrent`
)

func createStreamHandler(config config, searchClient *imdb2torrent.Client, rdClient *realdebrid.Client, adClient *alldebrid.Client, redirectCache *gocache.Cache, logger *zap.Logger) stremio.StreamHandler {
	return func(ctx context.Context, id string, userDataIface interface{}) ([]stremio.StreamItem, error) {
		torrents, err := searchClient.FindMagnets(ctx, id)
		if err != nil {
			logger.Warn("Couldn't find magnets", zap.Error(err))
			return nil, fmt.Errorf("Couldn't find magnets: %w", err)
		} else if len(torrents) == 0 {
			logger.Info("No magnets found")
			return nil, stremio.NotFound
		}

		// Parse userData.
		// No need to check if the interface is a string or if the decoding worked, because the token middleware does that already.
		udString := userDataIface.(string)
		userData, _ := decodeUserData(udString, logger)

		// Filter out the ones that are not available
		var infoHashes []string
		for _, torrent := range torrents {
			infoHashes = append(infoHashes, torrent.InfoHash)
		}
		var availableInfoHashes []string
		if userData.RDtoken != "" {
			availableInfoHashes = rdClient.CheckInstantAvailability(ctx, userData.RDtoken, infoHashes...)
		} else {
			availableInfoHashes = adClient.CheckInstantAvailability(ctx, userData.ADkey, infoHashes...)
		}
		if len(availableInfoHashes) == 0 {
			// TODO: queue for download on the debrid service, or log somewhere for an asynchronous process to go through them and queue them?
			logger.Info("None of the found torrents are instantly available on the debrid service")
			return nil, stremio.NotFound
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

		// Separate all torrent results into a 720p, 1080p, 1080p 10bit, 2160p and 2160p 10bit list, so we can offer the user one stream for each quality now (or maybe just for one quality if there's no torrent for the other), cache the torrents for each apiToken-imdbID-quality combination and later (at the redirect endpoint) go through the respective torrent list to turn it into a streamable video URL via RealDebrid.
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
				logger.Warn("Unknown quality, can't sort into one of the torrent lists", zap.String("quality", torrent.Quality))
			}
		}

		// Cache results to make this data available in the redirect handler. It will pick the first torrent from the list and convert it via RD, or pick the next if the previous didn't work.
		// There's no need to cache this for a specific user.
		// This cache *must* be a cache where items aren't evicted when the cache is full, because otherwise if the cache is full and two users fetch available streams, then the second one could lead to the first cache item being evicted before the first user clicks on the stream, leading to an error inside the redirect handler after he clicks on the stream.
		redirectCache.Set(id+"-720p", torrents720p, 0)
		redirectCache.Set(id+"-1080p", torrents1080p, 0)
		redirectCache.Set(id+"-1080p-10bit", torrents1080p10bit, 0)
		redirectCache.Set(id+"-2160p", torrents2160p, 0)
		redirectCache.Set(id+"-2160p-10bit", torrents2160p10bit, 0)

		// We already respond with several URLs (one for each quality, as long as we have torrents for the different qualities), but they point to our server for now.
		// Only when the user clicks on a stream and arrives at our redirect endpoint, we go through the list of torrents for the selected quality and try to convert them into a streamable video URL via RealDebrid.
		// There it should usually work for the first torrent we try, because we already checked the "instant availability" on RealDebrid here. If the "instant availability" info is stale (because we cached it), the next torrent will be used.
		var streams []stremio.StreamItem
		var requestIDPrefix string
		if userData.RDtoken != "" {
			requestIDPrefix = "rd-" + userData.RDtoken + "-" + strconv.FormatBool(userData.RDremote) + "-" + id
		} else {
			requestIDPrefix = "ad-" + userData.ADkey + "-" + id
		}
		if len(torrents720p) > 0 {
			stream := createStreamItem(ctx, config, requestIDPrefix+"-"+"720p", "720p", torrents720p)
			streams = append(streams, stream)
		}
		if len(torrents1080p) > 0 {
			stream := createStreamItem(ctx, config, requestIDPrefix+"-"+"1080p", "1080p", torrents1080p)
			streams = append(streams, stream)
		}
		if len(torrents1080p10bit) > 0 {
			stream := createStreamItem(ctx, config, requestIDPrefix+"-"+"1080p-10bit", "1080p 10bit", torrents1080p10bit)
			streams = append(streams, stream)
		}
		if len(torrents2160p) > 0 {
			stream := createStreamItem(ctx, config, requestIDPrefix+"-"+"2160p", "2160p", torrents2160p)
			streams = append(streams, stream)
		}
		if len(torrents2160p10bit) > 0 {
			stream := createStreamItem(ctx, config, requestIDPrefix+"-"+"2160p-10bit", "2160p 10bit", torrents2160p10bit)
			streams = append(streams, stream)
		}

		return streams, nil
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

func createRedirectHandler(redirectCache *gocache.Cache, rdClient *realdebrid.Client, adClient *alldebrid.Client, logger *zap.Logger) func(*fiber.Ctx) {
	return func(c *fiber.Ctx) {
		logger.Debug("redirectHandler called", zap.String("request", fmt.Sprintf("%+v", &c.Fasthttp.Request)))

		redirectID := c.Params("id", "")
		if redirectID == "" {
			c.SendStatus(fiber.StatusNotFound)
			return
		}
		zapFieldRedirectID := zap.String("redirectID", redirectID)

		idParts := strings.Split(redirectID, "-")
		// "<debridService>-<apiToken>-[<remote>]-<imdbID>-<quality>"
		if len(idParts) < 4 ||
			(idParts[0] == "rd" && len(idParts) != 5) ||
			(idParts[0] == "ad" && len(idParts) != 4) {
			c.SendStatus(fiber.StatusBadRequest)
			return
		}
		apiToken := idParts[1]
		var remote bool
		var imdbID string
		var quality string
		var err error
		if idParts[0] == "rd" {
			remote, err = strconv.ParseBool(idParts[2])
			if err != nil {
				logger.Error("Couldn't parse remote value", zap.Error(err), zapFieldRedirectID)
				c.SendStatus(fiber.StatusBadRequest)
				return
			}
			imdbID = idParts[3]
			quality = idParts[4]
		} else {
			imdbID = idParts[2]
			quality = idParts[3]
		}

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
			logger.Debug("Hit stream cache", zapFieldRedirectID)
			if streamURLitem, ok := streamURLiface.(cacheItem); !ok {
				logger.Error("Stream cache item couldn't be cast into cacheItem", zap.String("cacheItemType", fmt.Sprintf("%T", streamURLiface)), zapFieldRedirectID)
			} else if len(streamURLitem.Value) == 0 && time.Since(streamURLitem.Created) > time.Minute {
				logger.Warn("The torrents for this stream where previously tried to be converted into a stream but it didn't work. This was more than one minute ago though, so we'll try again.", zapFieldRedirectID)
			} else if len(streamURLitem.Value) == 0 {
				logger.Warn("The torrents for this stream where previously tried to be converted into a stream but it didn't work", zapFieldRedirectID)
				c.SendStatus(fiber.StatusNotFound)
				return
			} else {
				logger.Debug("Responding with redirect to stream", zap.String("redirectLocation", streamURLitem.Value), zapFieldRedirectID)
				c.Set("Location", streamURLitem.Value)
				c.SendStatus(fiber.StatusMovedPermanently)
				return
			}
		}

		// Here we get the data from the cache that the stream handler filled.
		torrentsIface, found := redirectCache.Get(imdbID + "-" + quality)
		if !found {
			logger.Warn(fmt.Sprintf("No torrents cache item found for %v, did 24h pass?", imdbID+"-"+quality), zapFieldRedirectID)
			// TODO: Just run the same stuff the stream handler does! This way we can drastically reduce the required cache time for the redirect cache, and the scraping doesn't really take long! Take care of concurrent requests - maybe lock!
			c.SendStatus(fiber.StatusNotFound)
			return
		}
		torrents, ok := torrentsIface.([]imdb2torrent.Result)
		if !ok {
			logger.Error("Torrents cache item couldn't be cast into []imdb2torrent.Result", zap.String("cacheItemType", fmt.Sprintf("%T", torrentsIface)), zapFieldRedirectID)
			c.SendStatus(fiber.StatusInternalServerError)
			return
		}
		var streamURL string
		for _, torrent := range torrents {
			if idParts[0] == "rd" {
				streamURL, err = rdClient.GetStreamURL(c.Context(), torrent.MagnetURL, apiToken, remote)
			} else {
				streamURL, err = adClient.GetStreamURL(c.Context(), torrent.MagnetURL, apiToken)
			}
			if err != nil {
				logger.Warn("Couldn't get stream URL", zap.Error(err), zapFieldRedirectID)
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
			c.SendStatus(fiber.StatusNotFound)
			return
		}

		logger.Debug("Responding with redirect to stream", zap.String("redirectLocation", streamURL), zapFieldRedirectID)
		c.Set("Location", streamURL)
		c.SendStatus(fiber.StatusMovedPermanently)
	}
}

func createStatusHandler(magnetSearchers map[string]imdb2torrent.MagnetSearcher, rdClient *realdebrid.Client, adClient *alldebrid.Client, goCaches map[string]*gocache.Cache, logger *zap.Logger) func(*fiber.Ctx) {
	return func(c *fiber.Ctx) {
		logger.Debug("statusHandler called", zap.String("request", fmt.Sprintf("%+v", &c.Fasthttp.Request)))

		imdbID := c.Query("imdbid", "")
		rdToken := c.Query("rdtoken", "")
		adKey := c.Query("adkey", "")
		if imdbID == "" || rdToken == "" || adKey == "" {
			logger.Warn("\"/status\" was called without IMDb ID or RD API token or AD API key")
			c.SendStatus(fiber.StatusBadRequest)
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
				results, err := goClient.Find(c.Context(), imdbID)
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
		streamURL, err := rdClient.GetStreamURL(c.Context(), bigBuckBunnyMagnet, rdToken, false)
		if err != nil {
			res += "\t\t" + `"err":"` + err.Error() + `",` + "\n"
		} else {
			res += "\t\t" + `"res":"` + streamURL + `",` + "\n"
		}
		durationRDmillis := time.Since(startRD).Milliseconds()
		res += "\t\t" + `"duration": "` + strconv.FormatInt(durationRDmillis, 10) + `ms"` + "\n"
		res += "\t" + `},` + "\n"

		// Check AD client

		res += "\t" + `"AD": {` + "\n"
		startAD := time.Now()
		streamURL, err = adClient.GetStreamURL(c.Context(), bigBuckBunnyMagnet, adKey)
		if err != nil {
			res += "\t\t" + `"err":"` + err.Error() + `",` + "\n"
		} else {
			res += "\t\t" + `"res":"` + streamURL + `",` + "\n"
		}
		durationADmillis := time.Since(startAD).Milliseconds()
		res += "\t\t" + `"duration": "` + strconv.FormatInt(durationADmillis, 10) + `ms"` + "\n"
		res += "\t" + `},` + "\n"

		// Check caches

		res += "\t" + `"caches": {` + "\n"
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

		logger.Debug("Responding", zap.String("response", res))
		c.Set("Content-Type", "application/json")
		c.SendString(res)
	}
}
