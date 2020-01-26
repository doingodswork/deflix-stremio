package imdb2torrent

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"

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

func (c Client) checkYTS(imdbID string) ([]Result, error) {
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
		return nil, fmt.Errorf("Couldn't find torrents")
	}
	title := gjson.GetBytes(resBody, "data.movies.0.title").String()
	results := []Result{}
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
