package imdb2torrent

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var (
	// See recommended tracker list on https://yts.mx/api#list_movies
	trackersYTS = []string{"udp://open.demonii.com:1337/announce",
		"udp://tracker.openbittorrent.com:80",
		"udp://tracker.coppersurfer.tk:6969",
		"udp://glotorrents.pw:6969/announce",
		"udp://tracker.opentrackr.org:1337/announce",
		"udp://torrent.gresille.org:80/announce",
		"udp://p4p.arenabg.com:1337",
		"udp://tracker.leechers-paradise.org:6969"}
)

type YTSclientOptions struct {
	BaseURL  string
	Timeout  time.Duration
	CacheAge time.Duration
}

func NewYTSclientOpts(baseURL string, timeout, cacheAge time.Duration) YTSclientOptions {
	return YTSclientOptions{
		BaseURL:  baseURL,
		Timeout:  timeout,
		CacheAge: cacheAge,
	}
}

var DefaultYTSclientOpts = YTSclientOptions{
	BaseURL:  "https://yts.mx",
	Timeout:  5 * time.Second,
	CacheAge: 24 * time.Hour,
}

var _ MagnetSearcher = (*ytsClient)(nil)

type ytsClient struct {
	baseURL    string
	httpClient *http.Client
	cache      Cache
	cacheAge   time.Duration
}

func NewYTSclient(ctx context.Context, opts YTSclientOptions, cache Cache) ytsClient {
	return ytsClient{
		baseURL: opts.BaseURL,
		httpClient: &http.Client{
			Timeout: opts.Timeout,
		},
		cache:    cache,
		cacheAge: opts.CacheAge,
	}
}

// Check uses YTS' API to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c ytsClient) Find(ctx context.Context, imdbID string) ([]Result, error) {
	logFields := log.Fields{
		"imdbID":      imdbID,
		"torrentSite": "YTS",
	}
	logger := log.WithContext(ctx).WithFields(logFields)

	// Check cache first
	cacheKey := imdbID + "-YTS"
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

	url := c.baseURL + "/api/v2/list_movies.json?query_term=" + imdbID
	res, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("Couldn't GET %v: %v", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bad GET response: %v", res.StatusCode)
	}
	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("Couldn't read response body: %v", err)
	}

	// Extract data from JSON
	torrents := gjson.GetBytes(resBody, "data.movies.0.torrents").Array()
	if len(torrents) == 0 {
		// Nil slice is ok, because it can be checked with len()
		return nil, nil
	}
	title := gjson.GetBytes(resBody, "data.movies.0.title").String()
	var results []Result
	for _, torrent := range torrents {
		quality := torrent.Get("quality").String()
		if quality == "720p" || quality == "1080p" || quality == "2160p" {
			infoHash := torrent.Get("hash").String()
			if infoHash == "" {
				logger.WithField("torrentJSON", torrent.String()).Warn("Couldn't get info_hash from torrent JSON")
				continue
			}
			magnetURL := createMagnetURL(ctx, infoHash, title, trackersYTS)
			ripType := torrent.Get("type").String()
			if ripType != "" {
				quality += " (" + ripType + ")"
			}
			logger.WithFields(log.Fields{"title": title, "quality": quality, "infoHash": infoHash, "magnet": magnetURL}).Trace("Found torrent")
			result := Result{
				Title:     title,
				Quality:   quality,
				InfoHash:  infoHash,
				MagnetURL: magnetURL,
			}
			results = append(results, result)
		}
	}

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if err := c.cache.Set(cacheKey, results); err != nil {
		logger.WithError(err).WithField("cache", "torrent").Error("Couldn't cache torrents")
	}

	return results, nil
}

func (c ytsClient) IsSlow() bool {
	return false
}
