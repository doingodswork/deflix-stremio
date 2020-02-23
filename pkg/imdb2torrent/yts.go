package imdb2torrent

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var (
	// See recommended tracker list on https://yts.mx/api#list_movies
	trackers = []string{"udp://open.demonii.com:1337/announce",
		"udp://tracker.openbittorrent.com:80",
		"udp://tracker.coppersurfer.tk:6969",
		"udp://glotorrents.pw:6969/announce",
		"udp://tracker.opentrackr.org:1337/announce",
		"udp://torrent.gresille.org:80/announce",
		"udp://p4p.arenabg.com:1337",
		"udp://tracker.leechers-paradise.org:6969"}
)

type ytsClient struct {
	baseURL    string
	httpClient *http.Client
	cache      *fastcache.Cache
}

func newYTSclient(ctx context.Context, baseURL string, timeout time.Duration, cache *fastcache.Cache) ytsClient {
	return ytsClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: cache,
	}
}

// check uses YTS' API to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c ytsClient) check(ctx context.Context, imdbID string) ([]Result, error) {
	// Check cache first
	cacheKey := imdbID + "-YTS"
	if torrentsGob, ok := c.cache.HasGet(nil, []byte(cacheKey)); ok {
		torrentList, created, err := FromCacheEntry(ctx, torrentsGob)
		if err != nil {
			log.Println("Couldn't decode YTS torrent results:", err)
		} else if time.Since(created) < (24 * time.Hour) {
			log.Printf("Hit cache for YTS torrents, returning %v results\n", len(torrentList))
			return torrentList, nil
		} else {
			log.Println("Hit cache for YTS torrents, but entry is expired since", time.Since(created.Add(24*time.Hour)))
		}
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
			result := createMagnetURL(ctx, infoHash, title)
			result.Quality = quality
			ripType := torrent.Get("type").String()
			if ripType != "" {
				result.Quality += " (" + ripType + ")"
			}
			results = append(results, result)
		}
	}

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if torrentsGob, err := NewCacheEntry(ctx, results); err != nil {
		log.Println("Couldn't create cache entry for YTS torrents:", err)
	} else {
		c.cache.Set([]byte(cacheKey), torrentsGob)
	}

	return results, nil
}

func createMagnetURL(ctx context.Context, infoHash, title string) Result {
	result := Result{
		InfoHash: infoHash,
		Title:    title,
	}

	result.MagnetURL = "magnet:?xt=urn:btih:" + infoHash + "&dn=" + url.QueryEscape(title)
	for _, tracker := range trackers {
		result.MagnetURL += "&tr" + tracker
	}
	return result
}
