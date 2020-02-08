package imdb2torrent

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/VictoriaMetrics/fastcache"
)

var (
	magnet2InfoHashRegex = regexp.MustCompile("btih:.+?&") // The "?" makes the ".+" non-greedy
)

type tpbClient struct {
	httpClient *http.Client
	cache      *fastcache.Cache
}

func newTPBclient(timeout time.Duration, cache *fastcache.Cache) tpbClient {
	return tpbClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: cache,
	}
}

// checkTPB scrapes TPB to find torrents for the given IMDb ID.
// TPB sometimes runs into a timeout, so let's allow multiple attempts *when a timeout occurs*.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c tpbClient) check(imdbID string, attempts int) ([]Result, error) {
	// Check cache first
	cacheKey := imdbID + "-TPB"
	if torrentsGob, ok := c.cache.HasGet(nil, []byte(cacheKey)); ok {
		torrentList, created, err := FromCacheEntry(torrentsGob)
		if err != nil {
			log.Println("Couldn't decode TPB torrent results:", err)
		} else if time.Since(created) < (24 * time.Hour) {
			log.Printf("Hit cache for TPB torrents, returning %v results\n", len(torrentList))
			return torrentList, nil
		} else {
			log.Println("Hit cache for TPB torrents, but entry is expired since", time.Since(created.Add(24*time.Hour)))
		}
	}

	if attempts == 0 {
		return nil, fmt.Errorf("Cannot check TPB with 0 attempts")
	}
	// "/0/7/207" suffix is: ? / sort by seeders / category "HD - Movies"
	reqUrl := "https://thepiratebay.org/search/" + imdbID + "/0/7/207"
	res, err := c.httpClient.Get(reqUrl)
	if err != nil {
		// HTTP client errors are *always* `*url.Error`s
		urlErr := err.(*url.Error)
		if urlErr.Timeout() {
			log.Println("Ran into a timeout for", reqUrl)
			if attempts == 1 {
				return nil, fmt.Errorf("All attempted requests to %v timed out", reqUrl)
			}
			// Just retrying again with the same HTTP client, which probably reuses the previous connection, doesn't work.
			// Simple tests have shown that when a proper connection exists, all requests to TPB work, while when no proper connection exists all requests time out.
			log.Println("Closing connections to TPB and retrying...")
			c.httpClient.CloseIdleConnections()
			return c.check(imdbID, attempts-1)
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
			log.Println("Scraped movie title is empty, did the HTML change?")
			return
		}
		quality := ""
		if strings.Contains(title, "720p") {
			quality = "720p"
		} else if strings.Contains(title, "1080p") {
			quality = "1080p"
		} else {
			return
		}
		title = strings.TrimSpace(title)

		// https://en.wikipedia.org/wiki/Pirated_movie_release_types
		if strings.Contains(title, "HDCAM") {
			quality += (" (⚠️cam)")
		} else if strings.Contains(title, "HDTS") {
			quality += (" (⚠️telesync)")
		}

		magnet, _ := s.Find(".detName").Next().Attr("href")
		if !strings.HasPrefix(magnet, "magnet:") {
			log.Println("Scraped magnet URL doesn't look like a magnet URL. Did the HTML change?")
			return
		}
		// look for "btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&" via regex and then cut out the hash
		match := magnet2InfoHashRegex.Find([]byte(magnet))
		infoHash := strings.TrimPrefix(string(match), "btih:")
		infoHash = strings.TrimSuffix(infoHash, "&")
		infoHash = strings.ToUpper(infoHash)

		result := Result{
			Title:     title,
			Quality:   quality,
			InfoHash:  infoHash,
			MagnetURL: magnet,
		}
		results = append(results, result)
	})

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if torrentsGob, err := NewCacheEntry(results); err != nil {
		log.Println("Couldn't create cache entry for TPB torrents:", err)
	} else {
		c.cache.Set([]byte(cacheKey), torrentsGob)
	}

	return results, nil
}
