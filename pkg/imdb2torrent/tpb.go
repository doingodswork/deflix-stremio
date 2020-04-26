package imdb2torrent

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/doingodswork/deflix-stremio/pkg/cinemata"
)

var (
	// See the trackers that TPB adds in each magnet to the info_hash received from apibay.org
	trackersTPB = []string{
		"udp://tracker.coppersurfer.tk:6969/announce",
		"udp://9.rarbg.to:2920/announce",
		"udp://tracker.opentrackr.org:1337",
		"udp://tracker.internetwarriors.net:1337/announce",
		"udp://tracker.leechers-paradise.org:6969/announce",
		"udp://tracker.coppersurfer.tk:6969/announce",
		"udp://tracker.pirateparty.gr:6969/announce",
		"udp://tracker.cyberia.is:6969/announce",
	}
)

type TPBclientOptions struct {
	BaseURL        string
	SocksProxyAddr string
	Timeout        time.Duration
	CacheAge       time.Duration
}

func NewTPBclientOpts(baseURL, socksProxyAddr string, timeout, cacheAge time.Duration) TPBclientOptions {
	return TPBclientOptions{
		BaseURL:        baseURL,
		SocksProxyAddr: socksProxyAddr,
		Timeout:        timeout,
		CacheAge:       cacheAge,
	}
}

var DefaultTPBclientOpts = TPBclientOptions{
	BaseURL:  "https://apibay.org",
	Timeout:  5 * time.Second,
	CacheAge: 24 * time.Hour,
}

var _ MagnetSearcher = (*tpbClient)(nil)

type tpbClient struct {
	baseURL        string
	httpClient     *http.Client
	cache          Cache
	cacheAge       time.Duration
	cinemataClient cinemata.Client
}

func NewTPBclient(ctx context.Context, opts TPBclientOptions, cache Cache, cinemataClient cinemata.Client) (tpbClient, error) {
	// Using a SOCKS5 proxy allows us to make requests to TPB via the TOR network
	var httpClient *http.Client
	if opts.SocksProxyAddr != "" {
		var err error
		if httpClient, err = newSOCKS5httpClient(opts.Timeout, opts.SocksProxyAddr); err != nil {
			return tpbClient{}, fmt.Errorf("Couldn't create HTTP client with SOCKS5 proxy: %v", err)
		}
	} else {
		httpClient = &http.Client{
			Timeout: opts.Timeout,
		}
	}
	return tpbClient{
		baseURL:        opts.BaseURL,
		httpClient:     httpClient,
		cache:          cache,
		cacheAge:       opts.CacheAge,
		cinemataClient: cinemataClient,
	}, nil
}

// Check cals the TPB API to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c tpbClient) Find(ctx context.Context, imdbID string) ([]Result, error) {
	logFields := log.Fields{
		"imdbID":      imdbID,
		"torrentSite": "TPB",
	}
	logger := log.WithContext(ctx).WithFields(logFields)

	// Check cache first
	cacheKey := imdbID + "-TPB"
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

	// Note: It seems that apibay.org has a "cat=" query parameter, but using the category 207 for "HD Movies" doesn't work (torrents for category 201 ("Movies") are returned as well).
	reqUrl := c.baseURL + "/q.php?q=" + imdbID
	res, err := c.httpClient.Get(reqUrl)
	if err != nil {
		return nil, fmt.Errorf("Couldn't GET %v: %v", reqUrl, err)
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
	torrents := gjson.ParseBytes(resBody).Array()
	if len(torrents) == 0 {
		// Nil slice is ok, because it can be checked with len()
		return nil, nil
	}

	// Get movie name
	movieName, _, err := c.cinemataClient.GetMovieNameYear(ctx, imdbID)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get movie name via Cinemata for IMDb ID %v: %v", imdbID, err)
	}

	var results []Result
	for _, torrent := range torrents {
		torrentName := torrent.Get("name").String()
		quality := ""
		if strings.Contains(torrentName, "720p") {
			quality = "720p"
		} else if strings.Contains(torrentName, "1080p") {
			quality = "1080p"
		} else if strings.Contains(torrentName, "2160p") {
			quality = "2160p"
		} else {
			continue
		}
		if strings.Contains(torrentName, "10bit") {
			quality += " 10bit"
		}
		// https://en.wikipedia.org/wiki/Pirated_movie_release_types
		if strings.Contains(torrentName, "HDCAM") {
			quality += (" (⚠️cam)")
		} else if strings.Contains(torrentName, "HDTS") || strings.Contains(torrentName, "HD-TS") {
			quality += (" (⚠️telesync)")
		}
		infoHash := torrent.Get("info_hash").String()
		if infoHash == "" {
			logger.WithField("torrentJSON", torrent.String()).Warn("Couldn't get info_hash from torrent JSON")
			continue
		}
		magnetURL := createMagnetURL(ctx, infoHash, movieName, trackersTPB)
		logger.WithFields(log.Fields{"title": movieName, "quality": quality, "infoHash": infoHash, "magnet": magnetURL}).Trace("Found torrent")
		result := Result{
			Title:     movieName,
			Quality:   quality,
			InfoHash:  infoHash,
			MagnetURL: magnetURL,
		}
		results = append(results, result)
	}

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if err := c.cache.Set(cacheKey, results); err != nil {
		logger.WithError(err).WithField("cache", "torrent").Error("Couldn't cache torrents")
	}

	return results, nil
}

func (c tpbClient) IsSlow() bool {
	return false
}
