package imdb2torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/VictoriaMetrics/fastcache"
	log "github.com/sirupsen/logrus"
)

type tpbClient struct {
	baseURL    string
	httpClient *http.Client
	cache      *fastcache.Cache
}

func newTPBclient(ctx context.Context, baseURL string, timeout time.Duration, cache *fastcache.Cache) tpbClient {
	return tpbClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: cache,
	}
}

// check scrapes TPB to find torrents for the given IMDb ID.
// TPB sometimes runs into a timeout, so let's allow multiple attempts *when a timeout occurs*.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c tpbClient) check(ctx context.Context, imdbID string, attempts int) ([]Result, error) {
	logFields := log.Fields{
		"imdbID":      imdbID,
		"torrentSite": "TPB",
	}
	logger := log.WithContext(ctx).WithFields(logFields)

	// Check cache first
	cacheKey := imdbID + "-TPB"
	if torrentsGob, ok := c.cache.HasGet(nil, []byte(cacheKey)); ok {
		torrentList, created, err := FromCacheEntry(ctx, torrentsGob)
		if err != nil {
			logger.WithError(err).Error("Couldn't decode torrent results")
		} else if time.Since(created) < (24 * time.Hour) {
			logger.WithField("torrentCount", len(torrentList)).Debug("Hit cache for torrents, returning results")
			return torrentList, nil
		} else {
			expiredSince := time.Since(created.Add(24 * time.Hour))
			logger.WithField("expiredSince", expiredSince).Debug("Hit cache for torrents, but entry is expired")
		}
	}

	if attempts == 0 {
		return nil, fmt.Errorf("Cannot check TPB with 0 attempts")
	}
	// "/0/7/207" suffix is: ? / sort by seeders / category "HD - Movies"
	reqUrl := c.baseURL + "/search/" + imdbID + "/0/7/207"
	res, err := c.httpClient.Get(reqUrl)
	if err != nil {
		// HTTP client errors are *always* `*url.Error`s
		urlErr := err.(*url.Error)
		if urlErr.Timeout() {
			logger.Info("Ran into a timeout")
			if attempts == 1 {
				return nil, fmt.Errorf("All attempted requests to %v timed out", reqUrl)
			}
			// Just retrying again with the same HTTP client, which probably reuses the previous connection, doesn't work.
			// Simple tests have shown that when a proper connection exists, all requests to TPB work, while when no proper connection exists all requests time out.
			logger.Debug("Closing connections to TPB and retrying...")
			c.httpClient.CloseIdleConnections()
			return c.check(ctx, imdbID, attempts-1)
		} else {
			return nil, fmt.Errorf("Couldn't GET %v: %v", reqUrl, err)
		}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bad GET response: %v", res.StatusCode)
	}

	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, fmt.Errorf("Couldn't load the HTML in goquery: %v", err)
	}

	// Find the review items
	// Note: Uses "double" and not "single" view!
	var results []Result
	doc.Find("tbody tr").Each(func(_ int, s *goquery.Selection) {
		title := s.Find(".detLink").Text()
		if title == "" {
			logger.Warn("Scraped movie title is empty, did the HTML change?")
			return
		}
		title = strings.TrimSpace(title)

		quality := ""
		if strings.Contains(title, "720p") {
			quality = "720p"
		} else if strings.Contains(title, "1080p") {
			quality = "1080p"
		} else if strings.Contains(title, "2160p") {
			quality = "2160p"
		} else {
			return
		}
		if strings.Contains(title, "10bit") {
			quality += " 10bit"
		}
		// https://en.wikipedia.org/wiki/Pirated_movie_release_types
		if strings.Contains(title, "HDCAM") {
			quality += (" (⚠️cam)")
		} else if strings.Contains(title, "HDTS") {
			quality += (" (⚠️telesync)")
		}

		magnet, _ := s.Find(".detName").Next().Attr("href")
		if !strings.HasPrefix(magnet, "magnet:") {
			logger.Warn("Scraped magnet URL doesn't look like a magnet URL. Did the HTML change?")
			return
		}
		// look for "btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&" via regex and then cut out the hash
		match := magnet2InfoHashRegex.Find([]byte(magnet))
		infoHash := strings.TrimPrefix(string(match), "btih:")
		infoHash = strings.TrimSuffix(infoHash, "&")
		infoHash = strings.ToUpper(infoHash)

		if infoHash == "" {
			logger.WithField("magnet", magnet).Warn("Couldn't extract info_hash. Did the HTML change?")
			return
		}

		result := Result{
			Title:     title,
			Quality:   quality,
			InfoHash:  infoHash,
			MagnetURL: magnet,
		}
		logger.WithFields(log.Fields{"title": title, "quality": quality, "infoHash": infoHash, "magnet": magnet}).Trace("Found torrent")
		results = append(results, result)
	})

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if torrentsGob, err := NewCacheEntry(ctx, results); err != nil {
		logger.WithError(err).WithField("cache", "torrent").Error("Couldn't create cache entry for torrents")
	} else {
		entrySize := strconv.Itoa(len(torrentsGob)/1024) + "KB"
		if len(torrentsGob) > 64*1024 {
			logger.WithField("cache", "torrent").WithField("entrySize", entrySize).Warn("New cacheEntry is bigger than 64KB, which means it won't be stored in the cache when calling fastcache's Set() method. SetBig() (and GetBig()) must be used instead!")
		} else {
			logger.WithField("cache", "torrent").WithField("entrySize", entrySize).Debug("Caching torrent results")
		}
		c.cache.Set([]byte(cacheKey), torrentsGob)
	}

	return results, nil
}
