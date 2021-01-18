package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	gocache "github.com/patrickmn/go-cache"
	"go.uber.org/zap"

	"github.com/deflix-tv/go-debrid/alldebrid"
	"github.com/deflix-tv/go-debrid/premiumize"
	"github.com/deflix-tv/go-debrid/realdebrid"
	"github.com/deflix-tv/go-stremio"
	"github.com/deflix-tv/imdb2torrent"
)

const (
	bigBuckBunnyMagnet = `magnet:?xt=urn:btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&dn=Big+Buck+Bunny&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.fastcast.nz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com&ws=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2F&xs=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2Fbig-buck-bunny.torrent`
)

// goCacher is a go-cache-compatible interface.
type goCacher interface {
	Set(string, interface{}, time.Duration)
	Get(string) (interface{}, bool)
}

func createStreamHandler(config config, searchClient *imdb2torrent.Client, rdClient *realdebrid.Client, adClient *alldebrid.Client, pmClient *premiumize.Client, redirectCache goCacher, isTVShow bool, logger *zap.Logger) stremio.StreamHandler {
	return func(ctx context.Context, id string, userDataIface interface{}) ([]stremio.StreamItem, error) {
		var imdbID string
		var season int
		var episode int
		var err error
		if isTVShow {
			idParts := strings.Split(id, ":")
			if len(idParts) != 3 {
				logger.Info("Stream handler for TV shows called without exactly 3 ID parts", zap.String("id", id))
				return nil, stremio.BadRequest
			}
			imdbID = idParts[0]
			season, err = strconv.Atoi(idParts[1])
			if err != nil {
				logger.Info("Couldn't convert season to int", zap.String("id", id))
				return nil, stremio.BadRequest
			}
			episode, err = strconv.Atoi(idParts[2])
			if err != nil {
				logger.Info("Couldn't convert episode to int", zap.String("id", id))
				return nil, stremio.BadRequest
			}
		} else {
			imdbID = id
		}

		var torrents []imdb2torrent.Result
		if isTVShow {
			torrents, err = searchClient.FindTVShow(ctx, imdbID, season, episode)
		} else {
			torrents, err = searchClient.FindMovie(ctx, imdbID)
		}
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
		var debridID string
		var availableInfoHashes []string
		keyOrToken := ctx.Value("deflix_keyOrToken").(string)
		if userData.RDtoken != "" || userData.RDoauth2 != "" {
			debridID = "rd"
			availableInfoHashes = rdClient.CheckInstantAvailability(ctx, keyOrToken, infoHashes...)
		} else if userData.ADkey != "" {
			debridID = "ad"
			availableInfoHashes = adClient.CheckInstantAvailability(ctx, keyOrToken, infoHashes...)
		} else {
			debridID = "pm"
			availableInfoHashes = pmClient.CheckInstantAvailability(ctx, keyOrToken, infoHashes...)
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

		// Separate all torrent results into a 720p, 1080p, 1080p 10bit, 2160p and 2160p 10bit list, so we can offer the user one stream for each quality now (or maybe just for one quality if there's no torrent for the other), cache the torrents for each apiToken-ID-quality combination and later (at the redirect endpoint) go through the respective torrent list to turn it into a streamable video URL via RealDebrid.
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

		// Cache results to make this data available in the redirect handler. It will pick the first torrent from the list and convert it via RD / AD / PM, or pick the next if the previous didn't work.
		// There's no need to cache this for a specific user, but it MUST be cached per debrid service - otherwise during concurrent requests, when a RD user goes to the redirect endpoint it could fetch torrents from the cache which are only available on AD / PM leading to a worse experience for the RD user.
		// This cache *must* be a cache where items aren't evicted when the cache is full, because otherwise if the cache is full and two users fetch available streams, then the second one could lead to the first cache item being evicted before the first user clicks on the stream, leading to an error inside the redirect handler after he clicks on the stream.
		redirectCache.Set(id+"-"+debridID+"-720p", torrents720p, redirectExpiration)
		redirectCache.Set(id+"-"+debridID+"-1080p", torrents1080p, redirectExpiration)
		redirectCache.Set(id+"-"+debridID+"-1080p.10bit", torrents1080p10bit, redirectExpiration)
		redirectCache.Set(id+"-"+debridID+"-2160p", torrents2160p, redirectExpiration)
		redirectCache.Set(id+"-"+debridID+"-2160p.10bit", torrents2160p10bit, redirectExpiration)

		// We already respond with several URLs (one for each quality, as long as we have torrents for the different qualities), but they point to our server for now.
		// Only when the user clicks on a stream and arrives at our redirect endpoint, we go through the list of torrents for the selected quality and try to convert them into a streamable video URL via RealDebrid.
		// There it should usually work for the first torrent we try, because we already checked the "instant availability" on RealDebrid here. If the "instant availability" info is stale (because we cached it), the next torrent will be used.
		var streams []stremio.StreamItem
		if len(torrents720p) > 0 {
			stream := createStreamItem(ctx, config, udString, id+"-"+debridID+"-720p", "720p", torrents720p)
			streams = append(streams, stream)
		}
		if len(torrents1080p) > 0 {
			stream := createStreamItem(ctx, config, udString, id+"-"+debridID+"-1080p", "1080p", torrents1080p)
			streams = append(streams, stream)
		}
		if len(torrents1080p10bit) > 0 {
			stream := createStreamItem(ctx, config, udString, id+"-"+debridID+"-1080p.10bit", "1080p 10bit", torrents1080p10bit)
			streams = append(streams, stream)
		}
		if len(torrents2160p) > 0 {
			stream := createStreamItem(ctx, config, udString, id+"-"+debridID+"-2160p", "2160p", torrents2160p)
			streams = append(streams, stream)
		}
		if len(torrents2160p10bit) > 0 {
			stream := createStreamItem(ctx, config, udString, id+"-"+debridID+"-2160p.10bit", "2160p 10bit", torrents2160p10bit)
			streams = append(streams, stream)
		}

		return streams, nil
	}
}

func createStreamItem(ctx context.Context, config config, encodedUserData string, redirectID, quality string, torrents []imdb2torrent.Result) stremio.StreamItem {
	// Path escaping required for TV shows, which contain ":"
	redirectID = url.PathEscape(redirectID)
	stream := stremio.StreamItem{
		URL: config.BaseURL + "/" + encodedUserData + "/redirect/" + redirectID,
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

func createRedirectHandler(redirectCache, streamCache goCacher, rdClient *realdebrid.Client, adClient *alldebrid.Client, pmClient *premiumize.Client, forwardOriginIP bool, logger *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		logger.Debug("redirectHandler called", zap.String("request", fmt.Sprintf("%+v", c.Request())))

		udString := c.Params("userData")
		redirectID := c.Params("id", "")
		if redirectID == "" {
			return c.SendStatus(fiber.StatusNotFound)
		}
		zapFieldRedirectID := zap.String("redirectID", redirectID)

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
		// Here we don't get the data that's passed from the stream handler to this redirect handler, but instead the the RD / AD / PM HTTP stream URL, which is cached after it was converted in a previous call.
		// This cache is important, because for a single click on a stream in Stremio there are multiple requests to this endpoint in a short timeframe.
		// This cache is also useful for when a user resumes his stream via Stremio after closing it. In this case the same RealDebrid HTTP stream must be delivered (or even if it would work with another one, using the same one would be beneficial).
		// Because the actual stream URLs are cached here, it MUST be user-specific! No need to use the full userData string though - we just hash it and use that as "user identifier".
		// TODO: Regarding stream resuming: We don't know how long RD / AD / PM HTTP stream URLs are valid. If it's shorter, we can shorten this as well. Also see similar TODO comment in main.go file.
		userHash := sha256.Sum256([]byte(udString))
		userHashEncoded := base64.RawURLEncoding.EncodeToString(userHash[:])
		streamCacheID := userHashEncoded + "-" + redirectID
		if streamURLiface, found := streamCache.Get(streamCacheID); found {
			logger.Debug("Hit stream cache", zapFieldRedirectID)
			if streamURLitem, ok := streamURLiface.(cacheItem); !ok {
				logger.Error("Stream cache item couldn't be cast into cacheItem", zap.String("cacheItemType", fmt.Sprintf("%T", streamURLiface)), zapFieldRedirectID)
			} else if len(streamURLitem.Value) == 0 && time.Since(streamURLitem.Created) > time.Minute {
				logger.Warn("The torrents for this stream where previously tried to be converted into a stream but it didn't work. This was more than one minute ago though, so we'll try again.", zapFieldRedirectID)
			} else if len(streamURLitem.Value) == 0 {
				logger.Warn("The torrents for this stream where previously tried to be converted into a stream but it didn't work", zapFieldRedirectID)
				return c.SendStatus(fiber.StatusNotFound)
			} else {
				logger.Debug("Responding with redirect to stream", zap.String("redirectLocation", streamURLitem.Value), zapFieldRedirectID)
				c.Set("Location", streamURLitem.Value)
				return c.SendStatus(fiber.StatusMovedPermanently)
			}
		}

		// Here we get the data from the cache that the stream handler filled.
		torrentsIface, found := redirectCache.Get(redirectID)
		if !found {
			logger.Warn("No torrents cache item found, did 24h pass?", zapFieldRedirectID)
			// TODO: Just run the same stuff the stream handler does! This way we can drastically reduce the required cache time for the redirect cache, and the scraping doesn't really take long! Take care of concurrent requests - maybe lock!
			return c.SendStatus(fiber.StatusNotFound)
		}
		torrents, ok := torrentsIface.([]imdb2torrent.Result)
		if !ok {
			logger.Error("Torrents cache item couldn't be cast into []imdb2torrent.Result", zap.String("cacheItemType", fmt.Sprintf("%T", torrentsIface)), zapFieldRedirectID)
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		// Parse userData.
		// No need to check if decoding worked, because the token middleware does that already.
		userData, _ := decodeUserData(udString, logger)
		var streamURL string
		var err error
		keyOrToken := c.Locals("deflix_keyOrToken").(string)
		if forwardOriginIP && len(c.IPs()) > 0 {
			c.Locals("debrid_originIP", c.IPs()[0])
		}
		for _, torrent := range torrents {
			if userData.RDtoken != "" || userData.RDoauth2 != "" {
				streamURL, err = rdClient.GetStreamURL(c.Context(), torrent.MagnetURL, keyOrToken, userData.RDremote)
			} else if userData.ADkey != "" {
				streamURL, err = adClient.GetStreamURL(c.Context(), torrent.MagnetURL, keyOrToken)
			} else {
				streamURL, err = pmClient.GetStreamURL(c.Context(), torrent.MagnetURL, keyOrToken)
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
		streamCache.Set(streamCacheID, streamURLitem, streamExpiration)

		if streamURL == "" {
			return c.SendStatus(fiber.StatusNotFound)
		}

		logger.Debug("Responding with redirect to stream", zap.String("redirectLocation", streamURL), zapFieldRedirectID)
		c.Set("Location", streamURL)
		return c.SendStatus(fiber.StatusMovedPermanently)
	}
}

func createStatusHandler(magnetSearchers map[string]imdb2torrent.MagnetSearcher, rdClient *realdebrid.Client, adClient *alldebrid.Client, pmClient *premiumize.Client, goCaches map[string]*gocache.Cache, forwardOriginIP bool, logger *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		logger.Debug("statusHandler called", zap.String("request", fmt.Sprintf("%+v", c.Request())))

		imdbID := c.Query("imdbid", "")
		rdToken := c.Query("rdtoken", "")
		adKey := c.Query("adkey", "")
		pmKey := c.Query("pmkey", "")
		if imdbID == "" || rdToken == "" || adKey == "" || pmKey == "" {
			logger.Warn("\"/status\" was called without IMDb ID or RD API token or AD API key or Premiumize API key")
			return c.SendStatus(fiber.StatusBadRequest)
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
				results, err := goClient.FindMovie(c.Context(), imdbID)
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

		// Check debrid clients

		if forwardOriginIP && len(c.IPs()) > 0 {
			c.Locals("debrid_originIP", c.IPs()[0])
		}

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

		// Check PM client

		res += "\t" + `"PM": {` + "\n"
		startPM := time.Now()
		streamURL, err = pmClient.GetStreamURL(c.Context(), bigBuckBunnyMagnet, pmKey)
		if err != nil {
			res += "\t\t" + `"err":"` + err.Error() + `",` + "\n"
		} else {
			res += "\t\t" + `"res":"` + streamURL + `",` + "\n"
		}
		durationPMmillis := time.Since(startPM).Milliseconds()
		res += "\t\t" + `"duration": "` + strconv.FormatInt(durationPMmillis, 10) + `ms"` + "\n"
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
		return c.SendString(res)
	}
}
