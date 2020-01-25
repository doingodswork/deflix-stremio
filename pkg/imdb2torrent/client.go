package imdb2torrent

import (
	"fmt"
	"io/ioutil"
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

type Client struct {
	httpClient *http.Client
	cache      map[string]Result
}

func NewClient(timeout time.Duration) Client {
	return Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: make(map[string]Result),
	}
}

func (c Client) FindMagnet(imdbID string) (Result, error) {
	result := Result{}

	// Check cache first
	if result, ok := c.cache[imdbID]; ok {
		return result, nil
	}

	// Check YTS
	url := "https://yts.lt/api/v2/list_movies.json?query_term=" + imdbID
	res, err := c.httpClient.Get(url)
	if err != nil {
		return result, fmt.Errorf("Couldn't GET %v: %v", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return result, fmt.Errorf("Bad GET response: %v", res.StatusCode)
	}
	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return result, fmt.Errorf("Couldn't read response body: %v", err)
	}

	// Extract data from JSON
	infoHash := gjson.GetBytes(resBody, "data.movies.0.torrents.0.hash").String()
	if infoHash == "" {
		return result, fmt.Errorf("Couldn't find torrent")
	}
	name := gjson.GetBytes(resBody, "data.movies.0.title").String()

	// Return magnet URL
	return createMagnetURL(infoHash, name), nil
}

type Result struct {
	Name      string
	MagnetURL string
}

func createMagnetURL(infoHash, name string) Result {
	result := Result{
		Name: name,
	}

	result.MagnetURL = "magnet:?xt=urn:btih:" + infoHash + "&dn=" + url.QueryEscape(name)
	for _, tracker := range trackers {
		result.MagnetURL += "&tr" + tracker
	}
	return result
}
