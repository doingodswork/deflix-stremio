package imdb2torrent

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/VictoriaMetrics/fastcache"
	"github.com/tidwall/gjson"
)

type leetxClient struct {
	httpClient *http.Client
	cache      *fastcache.Cache
}

func newLeetxclient(timeout time.Duration, cache *fastcache.Cache) leetxClient {
	return leetxClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: cache,
	}
}

// check scrapes 1337x to find torrents for the given IMDb ID.
// It uses the Stremio Cinemata remote addon to get a movie name for a given IMDb ID, so it can search 1337x with the name.
// If no error occured, but there are just no torrents for the movie yet, an empty result and *no* error are returned.
func (c leetxClient) check(imdbID string) ([]Result, error) {
	// Check cache first
	cacheKey := imdbID + "-1337x"
	if torrentsGob, ok := c.cache.HasGet(nil, []byte(cacheKey)); ok {
		torrentList, created, err := FromCacheEntry(torrentsGob)
		if err != nil {
			log.Println("Couldn't decode 1337x torrent results:", err)
		} else if time.Since(created) < (24 * time.Hour) {
			log.Printf("Hit cache for 1337x torrents, returning %v results\n", len(torrentList))
			return torrentList, nil
		} else {
			log.Println("Hit cache for 1337x torrents, but entry is expired since", time.Since(created.Add(24*time.Hour)))
		}
	}

	// Get movie name
	movieName, movieYear, err := c.getMovieName(imdbID)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get movie name via Cinemata for IMDb ID %v: %v", imdbID, err)
	}
	movieSearch := movieName
	if movieYear != "" {
		movieSearch += " " + movieYear
	}
	// Use this for general searching in URL "https://1337x.to/search/foo+bar/1/"
	//movieSearch = strings.Replace(movieSearch, " ", "+", -1)
	// Use this for *movie* searching in URL "https://1337x.to/category-search/foo%20bar/Movies/1/"
	movieSearch = url.QueryEscape(movieSearch)

	// Search on 1337x

	reqUrl := "https://1337x.to/category-search/" + movieSearch + "/Movies/1/"
	doc, err := c.getDoc(reqUrl)
	if err != nil {
		return nil, err
	}
	// Pick the first element, it's the most likely one to belong to the correct movie
	torrentPath, ok := doc.Find(".table-list tbody td a").Next().Attr("href")
	if !ok {
		return nil, fmt.Errorf("Couldn't find search result")
	}

	// Go via a single search result to the general movie page

	reqUrl = "https://1337x.to" + torrentPath
	doc, err = c.getDoc(reqUrl)
	if err != nil {
		return nil, err
	}
	// Find the general movie page URL
	movieInfoURL, ok := doc.Find(".content-row h3 a").Attr("href")
	if !ok {
		return nil, fmt.Errorf("Couldn't find search result")
	}

	// Go through torrent pages for the movie

	reqUrl = "https://1337x.to" + movieInfoURL
	doc, err = c.getDoc(reqUrl)
	if err != nil {
		return nil, err
	}
	var torrentPageURLs []string
	// Go through elements
	doc.Find(".table-list tbody tr").Each(func(i int, s *goquery.Selection) {
		linkText := s.Find("a").Next().Text()
		if strings.Contains(linkText, "720p") || strings.Contains(linkText, "1080p") {
			torrentLink, ok := s.Find("a").Next().Attr("href")
			if ok {
				torrentPageURLs = append(torrentPageURLs, "https://1337x.to"+torrentLink)
			}
		}
	})
	// TODO: We should differentiate between "parsing went wrong" and "just no search results".
	if len(torrentPageURLs) == 0 {
		return nil, nil
	}

	// Visit each torrent page *in parallel* and get the magnet URL

	resultChan := make(chan Result, len(torrentPageURLs))

	for _, torrentPageURL := range torrentPageURLs {
		go func(goTorrentPageURL string) {
			doc, err = c.getDoc(goTorrentPageURL)
			if err != nil {
				resultChan <- Result{}
				return
			}

			magnet, ok := doc.Find(".box-info ul li").First().Find("a").Attr("href")
			if !ok {
				resultChan <- Result{}
				return
			}

			quality := ""
			if strings.Contains(magnet, "720p") {
				quality = "720p"
			} else if strings.Contains(magnet, "1080p") {
				quality = "1080p"
			} else {
				// This should never be the case, because it was previously checked during scraping
				resultChan <- Result{}
				return
			}

			title := movieName

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

			result := Result{
				Title:     title,
				Quality:   quality,
				InfoHash:  infoHash,
				MagnetURL: magnet,
			}

			resultChan <- result
		}(torrentPageURL)
	}

	var results []Result
	// We don't use a timeout channel because the HTTP clients have a timeout so the goroutines are guaranteed to finish
	for i := 0; i < len(torrentPageURLs); i++ {
		results = append(results, <-resultChan)
	}

	// Fill cache, even if there are no results, because that's just the current state of the torrent site.
	// Any actual errors would have returned earlier.
	if torrentsGob, err := NewCacheEntry(results); err != nil {
		log.Println("Couldn't create cache entry for 1337x torrents:", err)
	} else {
		c.cache.Set([]byte(cacheKey), torrentsGob)
	}

	return results, nil
}

func (c leetxClient) getMovieName(imdbID string) (string, string, error) {
	reqUrl := "https://v3-cinemeta.strem.io/meta/movie/" + imdbID + ".json"

	res, err := c.httpClient.Get(reqUrl)
	if err != nil {
		return "", "", fmt.Errorf("Couldn't GET %v: %v", reqUrl, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Bad GET response: %v", res.StatusCode)
	}
	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", "", fmt.Errorf("Couldn't read response body: %v", err)
	}
	movieName := gjson.GetBytes(resBody, "meta.name").String()
	if movieName == "" {
		return "", "", fmt.Errorf("Couldn't find movie name in Cinemata response")
	}
	movieYear := gjson.GetBytes(resBody, "meta.year").String()

	return movieName, movieYear, nil
}

func (c leetxClient) getDoc(url string) (*goquery.Document, error) {
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
