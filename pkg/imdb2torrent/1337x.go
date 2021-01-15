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
	"go.uber.org/zap"
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
	baseURL          string
	httpClient       *http.Client
	cache            Cache
	metaGetter       MetaGetter
	cacheAge         time.Duration
	logger           *zap.Logger
	logFoundTorrents bool
}

func NewLeetxClient(opts LeetxClientOptions, cache Cache, metaGetter MetaGetter, logger *zap.Logger, logFoundTorrents bool) *leetxClient {
	return &leetxClient{
		baseURL: opts.BaseURL,
		httpClient: &http.Client{
			Timeout: opts.Timeout,
		},
		cache:            cache,
		metaGetter:       metaGetter,
		cacheAge:         opts.CacheAge,
		logger:           logger,
		logFoundTorrents: logFoundTorrents,
	}
}

// FindMovie scrapes 1337x to find torrents for the given IMDb ID.
// It uses the Stremio Cinemeta remote addon to get a movie name for a given IMDb ID, so it can search 1337x with the name.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c *leetxClient) FindMovie(ctx context.Context, imdbID string) ([]Result, error) {
	// Get movie name
	meta, err := c.metaGetter.GetMovieSimple(ctx, imdbID)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get movie name via Cinemeta for IMDb ID %v: %v", imdbID, err)
	}
	movieSearch := meta.Title
	if meta.Year != 0 {
		movieSearch += " " + strconv.Itoa(meta.Year)
	}
	movieSearch = url.PathEscape(movieSearch)

	urlPath := "category-search/" + movieSearch + "/Movies/1/"

	return c.find(ctx, imdbID, urlPath, meta.Title, false)
}

// FindTVShow scrapes 1337x to find torrents for the given IMDb ID + season + episode.
// It uses the Stremio Cinemeta remote addon to get a TV show name for a given IMDb ID, so it can search 1337x with the name.
// If no error occured, but there are just no torrents for the TV show yet, an empty result and *no* error are returned.
func (c *leetxClient) FindTVShow(ctx context.Context, imdbID string, season, episode int) ([]Result, error) {
	id := imdbID + ":" + strconv.Itoa(season) + ":" + strconv.Itoa(episode)
	meta, err := c.metaGetter.GetTVShowSimple(ctx, imdbID, season, episode)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get TV show title via Cinemeta for ID %v: %v", id, err)
	}
	tvShowSearch, err := createTVShowSearch(ctx, c.metaGetter, imdbID, season, episode)
	if err != nil {
		return nil, err
	}
	tvShowSearch = url.PathEscape(tvShowSearch)

	urlPath := "category-search/" + tvShowSearch + "/TV/1/"

	return c.find(ctx, id, urlPath, meta.Title, true)
}

func (c *leetxClient) find(ctx context.Context, id, urlPath, title string, isTVShow bool) ([]Result, error) {
	zapFieldID := zap.String("id", id)
	zapFieldTorrentSite := zap.String("torrentSite", "1337x")

	// Check cache first
	cacheKey := id + "-1337x"
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

	// Search on 1337x

	reqUrl := c.baseURL + "/" + urlPath
	origDoc, err := c.getDoc(ctx, reqUrl)
	if err != nil {
		return nil, err
	}
	// Pick the first element, it's the most likely one to belong to the correct movie / TV show
	torrentPath, ok := origDoc.Find(".table-list tbody td a").Next().Attr("href")
	if !ok {
		return nil, fmt.Errorf("Couldn't find search result")
	}

	// Try to go via the first search result to the general movie page. This guarantees that all torrents found on that page are definitive matches for the movie.
	// But this only works for movies, not for TV shows.
	// For movies, if we don't find the general movie page, we can always go back to the original search result page as well.
	// TODO: For TV shows we could try to go via the episode page.
	var docToSearch *goquery.Document
	if isTVShow {
		reqUrl = c.baseURL + torrentPath
		firstTorrentDoc, err := c.getDoc(ctx, reqUrl)
		if err != nil {
			c.logger.Warn("Couldn't get HTML doc for first torrent result", zap.Error(err), zapFieldID, zapFieldTorrentSite)
			docToSearch = origDoc
		} else {
			// Find the general movie page URL
			movieInfoURL, ok := firstTorrentDoc.Find(".content-row h3 a").Attr("href")
			// Only if this was found, we try to go through the torrent pages for the movie page
			if ok && movieInfoURL != "" {
				reqUrl = c.baseURL + movieInfoURL
				docToSearch, err = c.getDoc(ctx, reqUrl)
				if err != nil {
					// Only log, but continue - we can always use the results from the original search result page
					c.logger.Warn("Couldn't get HTML doc for general movie page", zap.Error(err), zapFieldID, zapFieldTorrentSite)
					docToSearch = origDoc
				}
			} else {
				docToSearch = origDoc
			}
		}
	} else {
		docToSearch = origDoc
	}
	// Go through elements
	var torrentPageURLs []string
	docToSearch.Find(".table-list tbody tr").Each(func(i int, s *goquery.Selection) {
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
			doc, err := c.getDoc(ctx, goTorrentPageURL)
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
			} else if len(infoHash) != 40 {
				c.logger.Warn("InfoHash isn't 40 characters long", zap.String("magnet", magnet), zapFieldID, zapFieldTorrentSite)
				resultChan <- Result{}
				return
			}

			result := Result{
				Title:     title,
				Quality:   quality,
				InfoHash:  infoHash,
				MagnetURL: magnet,
			}
			if c.logFoundTorrents {
				c.logger.Debug("Found torrent", zap.String("title", title), zap.String("quality", quality), zap.String("infoHash", infoHash), zap.String("magnet", magnet), zapFieldID, zapFieldTorrentSite)
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
