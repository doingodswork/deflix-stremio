package imdb2torrent

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"
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
	baseURL          string
	httpClient       *http.Client
	cache            Cache
	cacheAge         time.Duration
	metaGetter       MetaGetter
	logger           *zap.Logger
	logFoundTorrents bool
}

func NewTPBclient(opts TPBclientOptions, cache Cache, metaGetter MetaGetter, logger *zap.Logger, logFoundTorrents bool) (*tpbClient, error) {
	// Using a SOCKS5 proxy allows us to make requests to TPB via the TOR network
	var httpClient *http.Client
	if opts.SocksProxyAddr != "" {
		var err error
		if httpClient, err = newSOCKS5httpClient(opts.Timeout, opts.SocksProxyAddr); err != nil {
			return nil, fmt.Errorf("Couldn't create HTTP client with SOCKS5 proxy: %v", err)
		}
	} else {
		httpClient = &http.Client{
			Timeout: opts.Timeout,
		}
	}
	return &tpbClient{
		baseURL:          opts.BaseURL,
		httpClient:       httpClient,
		cache:            cache,
		cacheAge:         opts.CacheAge,
		metaGetter:       metaGetter,
		logger:           logger,
		logFoundTorrents: logFoundTorrents,
	}, nil
}

// FindMovie calls the TPB API to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c *tpbClient) FindMovie(ctx context.Context, imdbID string) ([]Result, error) {
	meta, err := c.metaGetter.GetMovieSimple(ctx, imdbID)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get movie title via Cinemeta for IMDb ID %v: %v", imdbID, err)
	}
	// Note: It seems that apibay.org has a "cat=" query parameter, but using the category 207 for "HD Movies" doesn't work (torrents for category 201 ("Movies") are returned as well).
	escapedQuery := imdbID
	return c.find(ctx, imdbID, meta.Title, escapedQuery)
}

// FindTVShow calls the TPB API to find torrents for the given IMDb ID + season + episode.
// If no error occured, but there are just no torrents for the TV show yet, an empty result and *no* error are returned.
func (c *tpbClient) FindTVShow(ctx context.Context, imdbID string, season, episode int) ([]Result, error) {
	id := imdbID + ":" + strconv.Itoa(season) + ":" + strconv.Itoa(episode)
	meta, err := c.metaGetter.GetTVShowSimple(ctx, imdbID, season, episode)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get TV show title via Cinemeta for ID %v: %v", id, err)
	}
	// TPB supports TV show search via IMDb ID, but 1. it requires the ID of the particular episode, which doesn't match what Stremio sends us,
	// and 2. in some quick tests the search results were much better (more results without sacrifice in quality) with title + season + episode.
	query, err := createTVShowSearch(ctx, c.metaGetter, imdbID, season, episode)
	if err != nil {
		return nil, err
	}
	queryEscaped := url.QueryEscape(query)
	// Category 205 is for "TV shows", 208 is for "HD - TV shows" and this indeed works (different from HD movies)
	queryEscaped += "&cat=208"
	return c.find(ctx, id, meta.Title, queryEscaped)
}

// Query must be URL-escaped already.
func (c *tpbClient) find(ctx context.Context, id, title, escapedQuery string) ([]Result, error) {
	zapFieldID := zap.String("id", id)
	zapFieldTorrentSite := zap.String("torrentSite", "TPB")

	// Check cache first
	cacheKey := id + "-TPB"
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

	reqUrl := c.baseURL + "/q.php?q=" + escapedQuery
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
			c.logger.Warn("Couldn't get info_hash from torrent JSON", zap.String("torrentJSON", torrent.String()), zapFieldID, zapFieldTorrentSite)
			continue
		} else if len(infoHash) != 40 {
			c.logger.Error("InfoHash isn't 40 characters long", zapFieldID, zapFieldTorrentSite)
			continue
		}
		magnetURL := createMagnetURL(ctx, infoHash, title, trackersTPB)
		if c.logFoundTorrents {
			c.logger.Debug("Found torrent", zap.String("title", title), zap.String("quality", quality), zap.String("infoHash", infoHash), zap.String("magnet", magnetURL), zapFieldID, zapFieldTorrentSite)
		}
		result := Result{
			Title:     title,
			Quality:   quality,
			InfoHash:  infoHash,
			MagnetURL: magnetURL,
		}
		results = append(results, result)
	}

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if err := c.cache.Set(cacheKey, results); err != nil {
		c.logger.Error("Couldn't cache torrents", zap.Error(err), zap.String("cache", "torrent"), zapFieldID, zapFieldTorrentSite)
	}

	return results, nil
}

func (c *tpbClient) IsSlow() bool {
	return false
}
