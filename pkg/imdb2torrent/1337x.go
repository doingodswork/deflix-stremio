package imdb2torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"go.uber.org/zap"

	"github.com/deflix-tv/go-stremio/pkg/cinemeta"
)

type LeetxClientOptions struct {
	BaseURL  string
	Timeout  time.Duration
	CacheAge time.Duration
}

func NewLeetxClientOpts(baseURL string, timeout, cacheAge time.Duration) LeetxClientOptions {
	return LeetxClientOptions{
		BaseURL:  baseURL,
		Timeout:  timeout,
		CacheAge: cacheAge,
	}
}

var DefaultLeetxClientOpts = LeetxClientOptions{
	BaseURL:  "https://1337x.to",
	Timeout:  5 * time.Second,
	CacheAge: 24 * time.Hour,
}

var _ MagnetSearcher = (*leetxClient)(nil)

type leetxClient struct {
	baseURL        string
	httpClient     *http.Client
	cache          Cache
	cinemetaClient *cinemeta.Client
	cacheAge       time.Duration
	logger         *zap.Logger
}

func NewLeetxClient(ctx context.Context, opts LeetxClientOptions, cache Cache, cinemetaClient *cinemeta.Client, logger *zap.Logger) *leetxClient {
	return &leetxClient{
		baseURL: opts.BaseURL,
		httpClient: &http.Client{
			Timeout: opts.Timeout,
		},
		cache:          cache,
		cinemetaClient: cinemetaClient,
		cacheAge:       opts.CacheAge,
		logger:         logger,
	}
}

// Find scrapes 1337x to find torrents for the given IMDb ID.
// It uses the Stremio Cinemeta remote addon to get a movie name for a given IMDb ID, so it can search 1337x with the name.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c *leetxClient) Find(ctx context.Context, imdbID string) ([]Result, error) {
	zapFieldID := zap.String("imdbID", imdbID)
	zapFieldTorrentSite := zap.String("torrentSite", "1337x")

	// Check cache first
	cacheKey := imdbID + "-1337x"
	torrentList, created, found, err := c.cache.Get(cacheKey)
	if err != nil {
		c.logger.Error("Couldn't get torrent results from cache", zap.Error(err), zapFieldID, zapFieldTorrentSite)
	} else if !found {
		c.logger.Debug("Torrent results not found in cache", zapFieldID, zapFieldTorrentSite)
	} else if time.Since(created) > (c.cacheAge) {
		expiredSince := time.Since(created.Add(c.cacheAge))
		c.logger.Debug("Hit cache for torrents, but item is expired", zap.Duration("expiredSince", expiredSince), zapFieldID, zapFieldTorrentSite)
	} else {
		c.logger.Debug("Hit cache for torrents, returning results", zap.Int("torrentCount", len(torrentList)), zapFieldID, zapFieldTorrentSite)
		return torrentList, nil
	}

	// Get movie name
	meta, err := c.cinemetaClient.GetMovie(ctx, imdbID)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get movie name via Cinemeta for IMDb ID %v: %v", imdbID, err)
	}
	movieSearch := meta.Name
	if meta.ReleaseInfo != "" {
		movieSearch += " " + meta.ReleaseInfo
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
				c.logger.Warn("Couldn't find link to the torrent page, did the HTML change?", zapFieldID, zapFieldTorrentSite)
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
		// Use configured base URL, which could be a proxy that we want to go through
		torrentPageURL, err = replaceURL(torrentPageURL, c.baseURL)
		if err != nil {
			c.logger.Warn("Couldn't replace URL which was retrieved from an HTML link", zap.Error(err), zapFieldID, zapFieldTorrentSite)
			continue
		}

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

			if infoHash == "" {
				c.logger.Warn("Couldn't extract info_hash. Did the HTML change?", zap.String("magnet", magnet), zapFieldID, zapFieldTorrentSite)
				resultChan <- Result{}
				return
			}

			result := Result{
				Title:     meta.Name,
				Quality:   quality,
				InfoHash:  infoHash,
				MagnetURL: magnet,
			}
			c.logger.Debug("Found torrent", zap.String("title", meta.Name), zap.String("quality", quality), zap.String("infoHash", infoHash), zap.String("magnet", magnet), zapFieldID, zapFieldTorrentSite)

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
	if err := c.cache.Set(cacheKey, results); err != nil {
		c.logger.Error("Couldn't cache torrents", zap.Error(err), zap.String("cache", "torrent"), zapFieldID, zapFieldTorrentSite)
	}

	return results, nil
}

func (c *leetxClient) IsSlow() bool {
	return false
}

func (c *leetxClient) getDoc(ctx context.Context, url string) (*goquery.Document, error) {
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
