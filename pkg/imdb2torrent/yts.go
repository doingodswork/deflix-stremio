package imdb2torrent

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/tidwall/gjson"
)

var (
	// See recommended tracker list on https://yts.lt/api#list_movies
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
	httpClient *http.Client
	cache      *cache
}

func newYTSclient(timeout time.Duration) ytsClient {
	return ytsClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: newCache(),
	}
}

// check uses YTS' API to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c ytsClient) check(imdbID string) ([]Result, error) {
	// Check cache first
	if results, ok := c.cache.get(imdbID); ok {
		log.Printf("Hit YTS client cache, returning %v results\n", len(results))
		return results, nil
	}

	url := "https://yts.lt/api/v2/list_movies.json?query_term=" + imdbID
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
		if quality == "720p" || quality == "1080p" {
			infoHash := torrent.Get("hash").String()
			result := createMagnetURL(infoHash, title)
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
	c.cache.set(imdbID, results)

	return results, nil
}

func createMagnetURL(infoHash, title string) Result {
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
