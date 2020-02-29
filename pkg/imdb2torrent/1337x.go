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

	"github.com/doingodswork/deflix-stremio/pkg/cinemata"
)

type leetxClient struct {
	baseURL        string
	httpClient     *http.Client
	cache          *fastcache.Cache
	cinemataClient cinemata.Client
}

func newLeetxclient(ctx context.Context, baseURL string, timeout time.Duration, cache *fastcache.Cache, cinemataClient cinemata.Client) leetxClient {
	return leetxClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache:          cache,
		cinemataClient: cinemataClient,
	}
}

// check scrapes 1337x to find torrents for the given IMDb ID.
// It uses the Stremio Cinemata remote addon to get a movie name for a given IMDb ID, so it can search 1337x with the name.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c leetxClient) check(ctx context.Context, imdbID string) ([]Result, error) {
	logFields := log.Fields{
		"imdbID":      imdbID,
		"torrentSite": "1337x",
	}
	logger := log.WithContext(ctx).WithFields(logFields)

	// Check cache first
	cacheKey := imdbID + "-1337x"
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

	// Get movie name
	movieName, movieYear, err := c.cinemataClient.GetMovieNameYear(ctx, imdbID)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get movie name via Cinemata for IMDb ID %v: %v", imdbID, err)
	}
	movieSearch := movieName
	if movieYear != 0 {
		movieSearch += " " + strconv.Itoa(movieYear)
	}
	// Use this for general searching in URL "https://1337x.to/search/foo+bar/1/"
	//movieSearch = strings.Replace(movieSearch, " ", "+", -1)
	// Use this for *movie* searching in URL "https://1337x.to/category-search/foo%20bar/Movies/1/"
	movieSearch = url.QueryEscape(movieSearch)

	// Search on 1337x

	reqUrl := c.baseURL + "/category-search/" + movieSearch + "/Movies/1/"
	doc, err := c.getDoc(ctx, reqUrl)
	if err != nil {
		return nil, err
	}
	// Pick the first element, it's the most likely one to belong to the correct movie
	torrentPath, ok := doc.Find(".table-list tbody td a").Next().Attr("href")
	if !ok {
		return nil, fmt.Errorf("Couldn't find search result")
	}

	// Go via a single search result to the general movie page

	reqUrl = c.baseURL + torrentPath
	doc, err = c.getDoc(ctx, reqUrl)
	if err != nil {
		return nil, err
	}
	// Find the general movie page URL
	movieInfoURL, ok := doc.Find(".content-row h3 a").Attr("href")
	if !ok {
		return nil, fmt.Errorf("Couldn't find search result")
	}

	// Go through torrent pages for the movie

	reqUrl = c.baseURL + movieInfoURL
	doc, err = c.getDoc(ctx, reqUrl)
	if err != nil {
		return nil, err
	}
	var torrentPageURLs []string
	// Go through elements
	doc.Find(".table-list tbody tr").Each(func(i int, s *goquery.Selection) {
		linkText := s.Find("a").Next().Text()
		if strings.Contains(linkText, "720p") || strings.Contains(linkText, "1080p") || strings.Contains(linkText, "2160p") {
			torrentLink, ok := s.Find("a").Next().Attr("href")
			if !ok || torrentLink == "" {
				logger.Warn("Couldn't find link to the torrent page, did the HTML change?")
				return
			}
			torrentPageURLs = append(torrentPageURLs, c.baseURL+torrentLink)
		}
	})
	// TODO: We should differentiate between "parsing went wrong" and "just no search results".
	if len(torrentPageURLs) == 0 {
		return nil, nil
	}

	// Visit each torrent page *in parallel* and get the magnet URL

	resultChan := make(chan Result, len(torrentPageURLs))

	for _, torrentPageURL := range torrentPageURLs {
		go func(goTorrentPageURL string) {
			doc, err = c.getDoc(ctx, goTorrentPageURL)
			if err != nil {
				resultChan <- Result{}
				return
			}

			magnet, ok := doc.Find(".box-info ul li").First().Find("a").Attr("href")
			if !ok || magnet == "" {
				resultChan <- Result{}
				return
			}

			title := movieName

			quality := ""
			if strings.Contains(magnet, "720p") {
				quality = "720p"
			} else if strings.Contains(magnet, "1080p") {
				quality = "1080p"
			} else if strings.Contains(magnet, "2160p") {
				quality = "2160p"
			} else {
				// This should never be the case, because it was previously checked during scraping
				resultChan <- Result{}
				return
			}

			if strings.Contains(magnet, "10bit") {
				quality += " 10bit"
			}

			// https://en.wikipedia.org/wiki/Pirated_movie_release_types
			if strings.Contains(magnet, "HDCam") {
				quality += (" (⚠️cam)")
			}

			// We should mark 1337x movies somehow, because we cannot be 100% sure it's the correct movie.
			// The quality might later be used as title, as suggested by Stremio.
			// (Albeit only in a specific case for a specific reason)
			quality += "\n(⚠️guessed match)"

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

			resultChan <- result
		}(torrentPageURL)
	}

	var results []Result
	// We don't use a timeout channel because the HTTP clients have a timeout so the goroutines are guaranteed to finish
	for i := 0; i < len(torrentPageURLs); i++ {
		result := <-resultChan
		if result.MagnetURL != "" {
			results = append(results, result)
		}
	}

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

func (c leetxClient) getDoc(ctx context.Context, url string) (*goquery.Document, error) {
	res, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("Couldn't GET %v: %v", url, err)
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

	return doc, nil
}
