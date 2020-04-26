package imdb2torrent

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	log "github.com/sirupsen/logrus"
)

var magnet2InfoHashRegexIbit = regexp.MustCompile(`btih:.+?\\x26dn=`) // The "?" makes the ".+" non-greedy

type IbitClientOptions struct {
	BaseURL  string
	Timeout  time.Duration
	CacheAge time.Duration
}

func NewIbitClientOpts(baseURL string, timeout, cacheAge time.Duration) IbitClientOptions {
	return IbitClientOptions{
		BaseURL:  baseURL,
		Timeout:  timeout,
		CacheAge: cacheAge,
	}
}

var DefaultIbitClientOpts = IbitClientOptions{
	BaseURL:  "https://ibit.am",
	Timeout:  5 * time.Second,
	CacheAge: 24 * time.Hour,
}

var _ MagnetSearcher = (*ibitClient)(nil)

type ibitClient struct {
	baseURL    string
	httpClient *http.Client
	cache      Cache
	lock       *sync.Mutex
	cacheAge   time.Duration
}

func NewIbitClient(ctx context.Context, opts IbitClientOptions, cache Cache) ibitClient {
	return ibitClient{
		baseURL: opts.BaseURL,
		httpClient: &http.Client{
			Timeout: opts.Timeout,
		},
		cache:    cache,
		lock:     &sync.Mutex{},
		cacheAge: opts.CacheAge,
	}
}

// Check scrapes ibit to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c ibitClient) Find(ctx context.Context, imdbID string) ([]Result, error) {
	// Lock for all requests to ibit, because of rate limiting
	c.lock.Lock()
	defer c.lock.Unlock()

	logFields := log.Fields{
		"imdbID":      imdbID,
		"torrentSite": "ibit",
	}
	logger := log.WithContext(ctx).WithFields(logFields)

	// Check cache first
	cacheKey := imdbID + "-ibit"
	torrentList, created, found, err := c.cache.Get(cacheKey)
	if err != nil {
		logger.WithError(err).Error("Couldn't get torrent results from cache")
	} else if !found {
		logger.Debug("Torrent results not found in cache")
	} else if time.Since(created) > (c.cacheAge) {
		expiredSince := time.Since(created.Add(c.cacheAge))
		logger.WithField("expiredSince", expiredSince).Debug("Hit cache for torrents, but item is expired")
	} else {
		logger.WithField("torrentCount", len(torrentList)).Debug("Hit cache for torrents, returning results")
		return torrentList, nil
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
			logger.Warn("Couldn't find link to the torrent page, did the HTML change?")
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

		// Use configured base URL, which could be a proxy that we want to go through
		torrentPageURL, err = replaceURL(torrentPageURL, c.baseURL)
		if err != nil {
			logger.WithError(err).Warn("Couldn't replace URL which was retrieved from an HTML link")
			continue
		}

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
		if magnet == "" {
			continue
		}

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
		// ibit changes their HTML sometimes, let's try another way if the previous one didn't yield a result
		if infoHash == "" {
			match = magnet2InfoHashRegexIbit.Find([]byte(magnet))
			infoHash = strings.TrimPrefix(string(match), "btih:")
			infoHash = strings.TrimSuffix(infoHash, `\x26dn=`)
			infoHash = strings.ReplaceAll(infoHash, "-", "")
			infoHash = strings.ToUpper(infoHash)
			// The rest of the magnet is also a bit "obfuscated" (they're using some hex characters, but not everywhere)
			if infoHash != "" {
				magnetTailIndex := strings.Index(magnet, `\x26tr=`)
				if magnetTailIndex == -1 {
					logger.WithField("magnet", magnet).Warn(`Couldn't recreate magnet URL by cutting at \x26tr=. Did the HTML change?`)
					continue
				}
				magnetTail := string(([]byte(magnet))[magnetTailIndex:])
				magnetTail = strings.ReplaceAll(magnetTail, `\x26`, "&")
				magnet = "magnet:?xt=urn:btih:" + infoHash + "&dn=" + url.QueryEscape(title) + magnetTail
			}
		}

		if infoHash == "" {
			logger.WithField("magnet", magnet).Warn("Couldn't extract info_hash. Did the HTML change?")
			continue
		}

		result := Result{
			Title:     title,
			Quality:   quality,
			InfoHash:  infoHash,
			MagnetURL: magnet,
		}
		logger.WithFields(log.Fields{"title": title, "quality": quality, "infoHash": infoHash, "magnet": magnet}).Trace("Found torrent")

		results = append(results, result)
	}

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if err := c.cache.Set(cacheKey, results); err != nil {
		logger.WithError(err).WithField("cache", "torrent").Error("Couldn't cache torrents")
	}

	return results, nil
}

func (c ibitClient) IsSlow() bool {
	return true
}
