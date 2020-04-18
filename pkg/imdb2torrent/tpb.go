package imdb2torrent

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/doingodswork/deflix-stremio/pkg/cinemata"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/proxy"
	"golang.org/x/net/publicsuffix"
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

var _ MagnetSearcher = (*tpbClient)(nil)

type tpbClient struct {
	baseURL        string
	httpClient     *http.Client
	cache          *fastcache.Cache
	cacheAge       time.Duration
	cinemataClient cinemata.Client
}

func NewTPBclient(ctx context.Context, baseURL, socksProxyAddr string, timeout time.Duration, cache *fastcache.Cache, cacheAge time.Duration, cinemataClient cinemata.Client) (tpbClient, error) {
	// Using a SOCKS5 proxy allows us to make requests to TPB via the TOR network
	var httpClient *http.Client
	if socksProxyAddr != "" {
		dialer, err := proxy.SOCKS5("tcp", socksProxyAddr, nil, proxy.Direct)
		if err != nil {
			return tpbClient{}, fmt.Errorf("Couldn't create SOCKS5 dialer: %v", err)
		}
		jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		if err != nil {
			return tpbClient{}, fmt.Errorf("Couldn't create cookie jar: %v", err)
		}
		httpClient = &http.Client{
			Transport: &http.Transport{
				Dial: dialer.Dial,
			},
			Jar:     jar,
			Timeout: timeout,
		}
	} else {
		httpClient = &http.Client{
			Timeout: timeout,
		}
	}
	return tpbClient{
		baseURL:        baseURL,
		httpClient:     httpClient,
		cache:          cache,
		cacheAge:       cacheAge,
		cinemataClient: cinemataClient,
	}, nil
}

// Check cals the TPB API to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c tpbClient) Check(ctx context.Context, imdbID string) ([]Result, error) {
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
		} else if time.Since(created) < (c.cacheAge) {
			logger.WithField("torrentCount", len(torrentList)).Debug("Hit cache for torrents, returning results")
			return torrentList, nil
		} else {
			expiredSince := time.Since(created.Add(c.cacheAge))
			logger.WithField("expiredSince", expiredSince).Debug("Hit cache for torrents, but entry is expired")
		}
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

func (c tpbClient) QuickSkip() bool {
	return false
}
