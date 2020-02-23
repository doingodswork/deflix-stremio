package imdb2torrent

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/VictoriaMetrics/fastcache"
)

type ibitClient struct {
	baseURL    string
	httpClient *http.Client
	cache      *fastcache.Cache
	lock       *sync.Mutex
}

func newIbitClient(baseURL string, timeout time.Duration, cache *fastcache.Cache) ibitClient {
	return ibitClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: cache,
		lock:  &sync.Mutex{},
	}
}

// check scrapes ibit to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c ibitClient) check(imdbID string) ([]Result, error) {
	// Lock for all requests to ibit, because of rate limiting
	c.lock.Lock()
	defer c.lock.Unlock()

	// Check cache first
	cacheKey := imdbID + "-ibit"
	if torrentsGob, ok := c.cache.HasGet(nil, []byte(cacheKey)); ok {
		torrentList, created, err := FromCacheEntry(torrentsGob)
		if err != nil {
			log.Println("Couldn't decode ibit torrent results:", err)
		} else if time.Since(created) < (24 * time.Hour) {
			log.Printf("Hit cache for ibit torrents, returning %v results\n", len(torrentList))
			return torrentList, nil
		} else {
			log.Println("Hit cache for ibit torrents, but entry is expired since", time.Since(created.Add(24*time.Hour)))
		}
	}

	reqUrl := c.baseURL + "/torrent-search/" + imdbID
	res, err := c.httpClient.Get(reqUrl)
	if err != nil {
		return nil, fmt.Errorf("Couldn't GET %v: %v", reqUrl, err)
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

	// Find the torrent page URLs
	var torrentPageURLs []string
	doc.Find(".torrents tr").Each(func(_ int, s *goquery.Selection) {
		torrentPageHref, ok := s.Find("a").Attr("href")
		if !ok || torrentPageHref == "" {
			log.Println("Couldn't find link to the torrent page, did the HTML change?")
			return
		}
		torrentPageURLs = append(torrentPageURLs, c.baseURL+torrentPageHref)
	})
	// TODO: We should differentiate between "parsing went wrong" and "just no search results".
	if len(torrentPageURLs) == 0 {
		return nil, nil
	}

	// Visit each torrent page *one after another* (ibit has rate limiting so concurrent requests don't work) and get the magnet URL

	var results []Result
	for _, torrentPageURL := range torrentPageURLs {
		// Sleeping 100ms between requests still leads to some `429 Too Many Requests` responses
		time.Sleep(150 * time.Millisecond)

		res, err := http.Get(torrentPageURL)
		if err != nil {
			continue
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			continue
		}

		// ibit puts the magnet link into the html body via JavaScript.
		// But the JS already contains the actual value, so we take it from there.
		body, err := ioutil.ReadAll(res.Body)
		if err != nil {
			continue
		}
		magnetBytes := regexMagnet.Find(body)
		magnet := strings.Trim(string(magnetBytes), "'")

		bodyReader := bytes.NewReader(body)
		doc, err = goquery.NewDocumentFromReader(bodyReader)
		if err != nil {
			continue
		}
		title := doc.Find("#extra-info h2 a").Text()
		if title == "" {
			continue
		}

		quality := ""
		if strings.Contains(magnet, "720p") {
			quality = "720p"
		} else if strings.Contains(magnet, "1080p") {
			quality = "1080p"
		} else if strings.Contains(magnet, "2160p") {
			quality = "2160p"
		} else {
			continue
		}

		if strings.Contains(magnet, "10bit") {
			quality += " 10bit"
		}

		// https://en.wikipedia.org/wiki/Pirated_movie_release_types
		if strings.Contains(magnet, "HDCAM") {
			quality += (" (⚠️cam)")
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
	}

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if torrentsGob, err := NewCacheEntry(results); err != nil {
		log.Println("Couldn't create cache entry for ibit torrents:", err)
	} else {
		c.cache.Set([]byte(cacheKey), torrentsGob)
	}

	return results, nil
}
