package imdb2torrent

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var (
	magnet2InfoHashRegex = regexp.MustCompile("btih:.+?&") // The "?" makes the ".+" non-greedy
)

// checkTPB scrapes TPB to find torrents for the given IMDb ID.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c Client) checkTPB(imdbID string) ([]Result, error) {
	// "/0/7/207" suffix is: ? / sort by seeders / category "HD - Movies"
	url := "https://thepiratebay.org/search/" + imdbID + "/0/7/207"
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

	// Find the review items
	// Note: Uses "double" and not "single" view!
	var results []Result
	doc.Find("tbody tr").Each(func(i int, s *goquery.Selection) {
		title := s.Find(".detLink").Text()
		quality := ""
		if strings.Contains(title, "720p") {
			quality = "720p"
		} else if strings.Contains(title, "1080p") {
			quality = "1080p"
		} else {
			return
		}
		title = strings.TrimSpace(title)

		// https://en.wikipedia.org/wiki/Pirated_movie_release_types
		if strings.Contains(title, "HDCAM") {
			quality += (" (cam)")
		} else if strings.Contains(title, "HDTS") {
			quality += (" (telesync)")
		}

		magnet, _ := s.Find(".detName").Next().Attr("href")
		// look for "btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&" via regex and then cut out the hash
		match := magnet2InfoHashRegex.Find([]byte(magnet))
		infoHash := strings.TrimPrefix(string(match), "btih:")
		infoHash = strings.TrimSuffix(infoHash, "&")
		infoHash = strings.ToUpper(infoHash)

		result := Result{
			Title:     title,
			Quality:   quality,
			InfoHash:  infoHash,
			MagnetURL: magnet,
		}
		results = append(results, result)
	})

	return results, nil
}
